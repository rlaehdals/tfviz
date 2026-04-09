package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

type TerraformPlan struct {
	FormatVersion    string            `json:"format_version"`
	TerraformVersion string            `json:"terraform_version"`
	PlannedValues    PlannedValues     `json:"planned_values"`
	ResourceChanges  []ResourceChange  `json:"resource_changes"`
	Configuration    PlanConfiguration `json:"configuration"`
}

type PlanConfiguration struct {
	RootModule ConfigModule `json:"root_module"`
}

type ConfigModule struct {
	Resources   []ConfigResource            `json:"resources,omitempty"`
	ModuleCalls map[string]ConfigModuleCall  `json:"module_calls,omitempty"`
	Outputs     map[string]ConfigOutput      `json:"outputs,omitempty"`
}

type ConfigOutput struct {
	Expression map[string]interface{} `json:"expression,omitempty"`
}

type ConfigModuleCall struct {
	Expressions map[string]interface{} `json:"expressions,omitempty"`
	Module      ConfigModule           `json:"module"`
}

type ConfigResource struct {
	Address     string                 `json:"address"`
	Type        string                 `json:"type"`
	Name        string                 `json:"name"`
	Expressions map[string]interface{} `json:"expressions"`
}

type PlannedValues struct {
	RootModule Module `json:"root_module"`
}

type Module struct {
	Address      string     `json:"address,omitempty"`
	Resources    []Resource `json:"resources,omitempty"`
	ChildModules []Module   `json:"child_modules,omitempty"`
}

type Resource struct {
	Address      string                 `json:"address"`
	Mode         string                 `json:"mode"`
	Type         string                 `json:"type"`
	Name         string                 `json:"name"`
	ProviderName string                 `json:"provider_name"`
	Values       map[string]interface{} `json:"values"`
}

type ResourceChange struct {
	Address       string `json:"address"`
	ModuleAddress string `json:"module_address,omitempty"`
	Mode          string `json:"mode"`
	Type          string `json:"type"`
	Name          string `json:"name"`
	ProviderName  string `json:"provider_name"`
	Change        Change `json:"change"`
}

type Change struct {
	Actions      []string               `json:"actions"`
	Before       map[string]interface{} `json:"before"`
	After        map[string]interface{} `json:"after"`
	AfterUnknown map[string]interface{} `json:"after_unknown"`
}

type AnalyzedPlan struct {
	Summary          PlanSummary      `json:"summary"`
	Modules          []ModuleAnalysis `json:"modules"`
	Timestamp        string           `json:"timestamp"`
	TerraformVersion string           `json:"terraform_version"`
}

type PlanSummary struct {
	TotalResources int            `json:"total_resources"`
	Actions        map[string]int `json:"actions"`
	Providers      []string       `json:"providers"`
}

type ModuleAnalysis struct {
	Address   string             `json:"address"`
	Resources []ResourceAnalysis `json:"resources"`
	Summary   ModuleSummary      `json:"summary"`
}

type ModuleSummary struct {
	ResourceCount int            `json:"resource_count"`
	Actions       map[string]int `json:"actions"`
	ResourceTypes map[string]int `json:"resource_types"`
}

type DiffLine struct {
	Type string
	Text string
}

type ResourceAnalysis struct {
	Address            string                 `json:"address"`
	Type               string                 `json:"type"`
	Name               string                 `json:"name"`
	Provider           string                 `json:"provider"`
	Action             string                 `json:"action"`
	Changes            []ChangeDetail         `json:"changes,omitempty"`
	Impact             string                 `json:"impact"`
	Description        string                 `json:"description"`
	DiffLines          []DiffLine             `json:"diff_lines"`
	PolicyDocumentJSON string                 `json:"policy_document_json,omitempty"`
	After              map[string]interface{} `json:"after,omitempty"`

	DependsOn []string `json:"depends_on,omitempty"`
}

type ChangeDetail struct {
	Field  string      `json:"field"`
	Before interface{} `json:"before"`
	After  interface{} `json:"after"`
	Action string      `json:"action"`
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	command := os.Args[1]
	args := os.Args[2:]

	if command == "plan" {
		handlePlan(args)
	} else if command == "demo" {
		if len(args) < 1 {
			fmt.Println("Usage: tfviz demo <json-file>")
			os.Exit(1)
		}
		generateHTMLFromJSON(args[0], true)
	} else {
		fmt.Println("❗️ Unsupported command:", command)
		fmt.Println("Please use 'tfviz plan' to generate a plan visualization.")
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`tfviz - Terraform Plan Visualizer

Usage:
  tfviz plan [options]    Run terraform plan and generate HTML visualization
`)
}

func handlePlan(args []string) {
	planBinaryFile := "tfplan"

	showGraph := false
	filtered := []string{}
	for _, a := range args {
		if a == "--graph" || a == "-g" {
			showGraph = true
			continue
		}
		filtered = append(filtered, a)
	}
	args = filtered

	fmt.Println("🔄 Running terraform plan...")
	planArgs := append([]string{"plan", "-out=" + planBinaryFile}, args...)
	cmd := exec.Command("terraform", planArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("❌ Error running terraform plan: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("📄 Extracting JSON from plan...")
	showCmd := exec.Command("terraform", "show", "-json", planBinaryFile)
	out, err := showCmd.Output()
	if err != nil {
		fmt.Printf("❌ Error running terraform show: %v\n", err)
		os.Exit(1)
	}

	var plan TerraformPlan
	err = json.Unmarshal(out, &plan)
	if err != nil {
		fmt.Printf("❌ Error parsing JSON plan: %v\n", err)
		os.Exit(1)
	}

	analyzed := analyzePlan(plan)
	refEdges := buildRefEdges(plan.Configuration)
	containment := buildContainmentMap(plan.Configuration)
	allPlanned := collectAllPlannedResources(plan.PlannedValues.RootModule)
	enrichContainmentFromValues(allPlanned, containment)
	plannedValues := buildPlannedValuesMap(allPlanned)
	html := generateHTML(analyzed, showGraph, refEdges, containment, plannedValues)

	err = os.Remove(planBinaryFile)
	if err != nil {
		fmt.Printf("❌ Error deleting plan file %s: %v\n", planBinaryFile, err)
	} else {
		fmt.Println("✅ Plan file deleted successfully")
	}

	serveHTMLOnce(html)
}

func generateHTMLFromJSON(planFile string, showGraph bool) {
	fmt.Println("📊 Analyzing terraform plan...")

	data, err := os.ReadFile(planFile)
	if err != nil {
		fmt.Printf("❌ Error reading plan file: %v\n", err)
		return
	}

	var plan TerraformPlan
	err = json.Unmarshal(data, &plan)
	if err != nil {
		fmt.Printf("❌ Error parsing JSON plan: %v\n", err)
		return
	}

	analyzed := analyzePlan(plan)
	refEdges := buildRefEdges(plan.Configuration)
	containment := buildContainmentMap(plan.Configuration)
	allPlanned := collectAllPlannedResources(plan.PlannedValues.RootModule)
	enrichContainmentFromValues(allPlanned, containment)
	plannedValues := buildPlannedValuesMap(allPlanned)
	html := generateHTML(analyzed, showGraph, refEdges, containment, plannedValues)
	serveHTMLOnce(html)
}

func serveHTMLOnce(html string) {
	port := "9876"
	url := "http://localhost:" + port

	go func() {
		time.Sleep(300 * time.Millisecond)
		openBrowser(url)
	}()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, html)
		go func() {
			time.Sleep(5 * time.Second)
			os.Exit(0)
		}()
	})

	fmt.Println("🚀 Preview opened in browser. The server will shut down automatically.")
	err := http.ListenAndServe(":"+port, nil)
	if err != nil {
		fmt.Printf("❌ HTTP server error: %v\n", err)
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}

	cmd.Start()
}

func analyzePlan(plan TerraformPlan) AnalyzedPlan {
	analyzed := AnalyzedPlan{
		Summary: PlanSummary{
			Actions:   make(map[string]int),
			Providers: []string{},
		},
		Modules:          []ModuleAnalysis{},
		Timestamp:        time.Now().Format("2006-01-02 15:04:05"),
		TerraformVersion: plan.TerraformVersion,
	}

	providerSet := make(map[string]bool)
	moduleMap := map[string]*ModuleAnalysis{}

	for _, rc := range plan.ResourceChanges {
		action := "no-op"
		if len(rc.Change.Actions) > 0 {
    		if len(rc.Change.Actions) == 2 && rc.Change.Actions[0] == "delete" && rc.Change.Actions[1] == "create" {
        		action = "update"
    		} else {
        		action = rc.Change.Actions[0]
    		}
		}
		analyzed.Summary.Actions[action]++
		providerSet[rc.ProviderName] = true

		modAddr := rc.ModuleAddress
		if modAddr == "" {
			modAddr = "root"
		}
		if _, exists := moduleMap[modAddr]; !exists {
			moduleMap[modAddr] = &ModuleAnalysis{
				Address: modAddr,
				Summary: ModuleSummary{
					Actions:       make(map[string]int),
					ResourceTypes: make(map[string]int),
				},
			}
		}

		res := ResourceAnalysis{
			Address:     rc.Address,
			Type:        rc.Type,
			Name:        rc.Name,
			Provider:    rc.ProviderName,
			Action:      action,
			Impact:      determineImpact(action, rc.Type),
			Description: generateDescription(action, rc.Type, rc.Name),
			After:       rc.Change.After,
		}

		if depVal, ok := rc.Change.After["depends_on"]; ok {
			switch deps := depVal.(type) {
			case []interface{}:
				for _, d := range deps {
					if s, ok := d.(string); ok {
						res.DependsOn = append(res.DependsOn, s)
					}
				}
			case []string:
				res.DependsOn = append(res.DependsOn, deps...)
			}
		}

		if policyVal, ok := rc.Change.After["policy"]; ok {
			if policyStr, isString := policyVal.(string); isString {
				var parsedPolicy interface{}
				err := json.Unmarshal([]byte(policyStr), &parsedPolicy)
				if err == nil {
					prettyPolicy, err := json.MarshalIndent(parsedPolicy, "", "  ")
					if err == nil {
						res.PolicyDocumentJSON = string(prettyPolicy)
					}
				}
			}
		}
		if assumeRolePolicyVal, ok := rc.Change.After["assume_role_policy"]; ok {
			if assumeRolePolicyStr, isString := assumeRolePolicyVal.(string); isString {
				var parsedAssumeRolePolicy interface{}
				err := json.Unmarshal([]byte(assumeRolePolicyStr), &parsedAssumeRolePolicy)
				if err == nil {
					prettyAssumeRolePolicy, err := json.MarshalIndent(parsedAssumeRolePolicy, "", "  ")
					if err == nil {
						res.PolicyDocumentJSON = string(prettyAssumeRolePolicy)
					}
				}
			}
		}

		res.Changes = analyzeChanges(rc.Change.Before, rc.Change.After)

		isReplace := len(rc.Change.Actions) == 2 && rc.Change.Actions[0] == "delete" && rc.Change.Actions[1] == "create"
		res.DiffLines = generateTerraformStyleDiff(rc, isReplace)

		m := moduleMap[modAddr]
		m.Resources = append(m.Resources, res)
		m.Summary.ResourceCount++
		m.Summary.Actions[action]++
		m.Summary.ResourceTypes[rc.Type]++
	}

	for p := range providerSet {
		analyzed.Summary.Providers = append(analyzed.Summary.Providers, p)
	}

	modules := []ModuleAnalysis{}
	for _, m := range moduleMap {
		sort.SliceStable(m.Resources, func(i, j int) bool {
			if m.Resources[i].Action == "no-op" && m.Resources[j].Action != "no-op" {
				return false
			} else if m.Resources[i].Action != "no-op" && m.Resources[j].Action == "no-op" {
				return true
			}
			return m.Resources[i].Address < m.Resources[j].Address
		})

		if hasChanges(*m) {
			modules = append(modules, *m)
		}
	}

	sort.SliceStable(modules, func(i, j int) bool {
		iChanged := hasChanges(modules[i])
		jChanged := hasChanges(modules[j])
		if iChanged && !jChanged {
			return true
		} else if !iChanged && jChanged {
			return false
		}
		return modules[i].Address < modules[j].Address
	})
	total := 0
	for _, m := range modules {
		total += len(m.Resources)
	}
	analyzed.Summary.TotalResources = total

	analyzed.Modules = modules
	return analyzed
}

func hasChanges(m ModuleAnalysis) bool {
	for _, r := range m.Resources {
		if r.Action != "no-op" {
			return true
		}
	}
	return false
}

func analyzeChanges(before, after map[string]interface{}) []ChangeDetail {
	var changes []ChangeDetail

	for key, afterValue := range after {
		beforeValue, existsBefore := before[key]

		var action string
		if !existsBefore {
			action = "add"
		} else if !deepEqual(beforeValue, afterValue) {
			action = "update"
		} else {
			continue
		}

		changes = append(changes, ChangeDetail{
			Field:  key,
			Before: beforeValue,
			After:  afterValue,
			Action: action,
		})
	}

	for key, beforeValue := range before {
		if _, exists := after[key]; !exists {
			changes = append(changes, ChangeDetail{
				Field:  key,
				Before: beforeValue,
				After:  nil,
				Action: "remove",
			})
		}
	}

	return changes
}

func deepEqual(a, b interface{}) bool {
	ajson, err1 := json.Marshal(a)
	bjson, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ajson) == string(bjson)
}

func determineImpact(action, resourceType string) string {
	switch action {
	case "create":
		return "Low"
	case "update":
		if strings.Contains(resourceType, "security_group") ||
			strings.Contains(resourceType, "iam") ||
			strings.Contains(resourceType, "policy") {
			return "High"
		}
		return "Medium"
	case "delete", "destroy", "remove":
		return "High"
	default:
		return "Low"
	}
}

func generateDescription(action, resourceType, name string) string {
	switch action {
	case "create", "add":
		return fmt.Sprintf("%s '%s' Create", resourceType, name)
	case "update":
		return fmt.Sprintf("%s '%s' Update ", resourceType, name)
	case "delete", "destroy", "remove":
		return fmt.Sprintf("Delete %s '%s'", resourceType, name)
	default:
		return fmt.Sprintf("%s '%s' Unchanged", resourceType, name)
	}
}

func generateTerraformStyleDiff(rc ResourceChange, isReplace bool) []DiffLine {
	var lines []DiffLine

	actionPrefix := " "
	switch rc.Change.Actions[0] {
	case "create":
		actionPrefix = "+"
	case "delete":
		actionPrefix = "-"
	case "update":
		actionPrefix = "~"
	}

	lines = append(lines, DiffLine{Type: "header", Text: fmt.Sprintf("%s resource \"%s\" \"%s\" {", actionPrefix, rc.Type, rc.Name)})

	diffAttributes(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown, isReplace, 1, &lines)

	lines = append(lines, DiffLine{Type: "header", Text: "}"})

	return lines
}

func diffAttributes(before, after, afterUnknown map[string]interface{}, isReplace bool, indentLevel int, lines *[]DiffLine) {
	indent := strings.Repeat("  ", indentLevel)
	allKeys := uniqueSortedKeys(before, after, afterUnknown)

	for _, key := range allKeys {
		bv, bOk := before[key]
		av, aOk := after[key]
		auv, auOk := afterUnknown[key]

		comment := ifReplaceComment(isReplace)

		if auOk {
			if bVal, isBool := auv.(bool); isBool && bVal {
				if bOk {
					*lines = append(*lines, DiffLine{Type: "modified", Text: fmt.Sprintf("%s  %s = %s => (known after apply)%s", indent, key, formatValue(bv), comment)})
				} else {
					*lines = append(*lines, DiffLine{Type: "added", Text: fmt.Sprintf("%s+ %s = (known after apply)%s", indent, key, comment)})
				}
				continue
			}
		}

		if bOk && !aOk {
			*lines = append(*lines, DiffLine{Type: "removed", Text: fmt.Sprintf("%s- %s = %s%s", indent, key, formatValue(bv), comment)})
		} else if !bOk && aOk {
			*lines = append(*lines, DiffLine{Type: "added", Text: fmt.Sprintf("%s+ %s = %s%s", indent, key, formatValue(av), comment)})
		} else if bOk && aOk && !deepEqual(bv, av) {
			if bMap, bIsMap := bv.(map[string]interface{}); bIsMap {
				if aMap, aIsMap := av.(map[string]interface{}); aIsMap {
					*lines = append(*lines, DiffLine{Type: "modified", Text: fmt.Sprintf("%s  %s {", indent, key)})
					diffAttributes(bMap, aMap, afterUnknown, isReplace, indentLevel+1, lines)
					*lines = append(*lines, DiffLine{Type: "modified", Text: fmt.Sprintf("%s}", indent)})
					continue
				}
			}
			*lines = append(*lines, DiffLine{Type: "modified", Text: fmt.Sprintf("%s~ %s = %s => %s%s", indent, key, formatValue(bv), formatValue(av), comment)})
		} else {
			*lines = append(*lines, DiffLine{Type: "unchanged", Text: fmt.Sprintf("%s  %s = %s", indent, key, formatValue(av))})
		}
	}
}

func extractReferences(v interface{}) []string {
	var refs []string
	switch val := v.(type) {
	case map[string]interface{}:
		if refList, ok := val["references"]; ok {
			if arr, ok := refList.([]interface{}); ok {
				for _, item := range arr {
					if s, ok := item.(string); ok {
						refs = append(refs, s)
					}
				}
			}
		}
		for _, child := range val {
			refs = append(refs, extractReferences(child)...)
		}
	case []interface{}:
		for _, child := range val {
			refs = append(refs, extractReferences(child)...)
		}
	}
	return refs
}

func normalizeRef(ref string) string {
	parts := strings.Split(ref, ".")
	for i, p := range parts {
		if strings.Contains(p, "_") && i+1 < len(parts) {
			return strings.Join(parts[:i+2], ".")
		}
	}
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return ref
}

var indexSuffixRe = regexp.MustCompile(`\[[^\]]+\]`)

func stripIndex(addr string) string {
	return indexSuffixRe.ReplaceAllString(addr, "")
}

func resolveRef(ref string, modulePrefix string, varMap map[string]string) string {
	parts := strings.Split(ref, ".")
	if len(parts) < 2 {
		return ""
	}
	first := parts[0]

	if first == "var" && varMap != nil {
		varName := parts[1]
		if resolved, ok := varMap[varName]; ok {
			return resolved
		}
		return ""
	}
	if first == "local" || first == "each" || first == "data" {
		return ""
	}

	normalized := normalizeRef(ref)
	if strings.HasPrefix(normalized, "module.") {
		return normalized
	}
	if modulePrefix != "" {
		return modulePrefix + "." + normalized
	}
	return normalized
}

func isModuleOnlyPrefix(addr string) bool {
	parts := strings.Split(addr, ".")
	if len(parts) < 2 || parts[0] != "module" {
		return false
	}
	for _, p := range parts {
		if strings.Contains(p, "_") {
			return false
		}
	}
	return true
}

func buildModuleOutputMap(config PlanConfiguration) map[string]string {
	result := map[string]string{}
	collectOutputs(config.RootModule, "", result)
	return result
}

func collectOutputs(mod ConfigModule, modulePrefix string, result map[string]string) {
	for callName, call := range mod.ModuleCalls {
		childPrefix := "module." + callName
		if modulePrefix != "" {
			childPrefix = modulePrefix + ".module." + callName
		}

		for outputName, output := range call.Module.Outputs {
			refs := getDirectRefs(output.Expression)
			for _, ref := range refs {
				parts := strings.Split(ref, ".")
				if len(parts) < 2 {
					continue
				}
				if parts[0] == "var" || parts[0] == "local" || parts[0] == "each" {
					continue
				}
				normalized := normalizeRef(ref)
				if !strings.HasPrefix(normalized, "module.") {
					normalized = childPrefix + "." + normalized
				}
				result[childPrefix+"."+outputName] = normalized
				break
			}
		}

		collectOutputs(call.Module, childPrefix, result)
	}
}

func buildVarMap(callExprs map[string]interface{}, outputMap map[string]string) map[string]string {
	result := map[string]string{}
	for varName, expr := range callExprs {
		refs := getDirectRefs(expr)
		for _, ref := range refs {
			parts := strings.Split(ref, ".")
			if len(parts) < 2 {
				continue
			}
			first := parts[0]
			if first == "var" || first == "local" || first == "each" || first == "data" {
				continue
			}

			if outputMap != nil {
				if addr, ok := outputMap[ref]; ok {
					result[varName] = addr
					break
				}
			}

			normalized := normalizeRef(ref)
			if !isModuleOnlyPrefix(normalized) {
				result[varName] = normalized
				break
			}
		}
	}
	return result
}

func propagateParentVars(callExprs map[string]interface{}, parentVarMap map[string]string, childVarMap map[string]string) {
	if parentVarMap == nil {
		return
	}
	for varName, expr := range callExprs {
		if _, exists := childVarMap[varName]; exists {
			continue
		}
		refs := getDirectRefs(expr)
		for _, ref := range refs {
			parts := strings.Split(ref, ".")
			if len(parts) >= 2 && parts[0] == "var" {
				if resolved, ok := parentVarMap[parts[1]]; ok {
					childVarMap[varName] = resolved
					break
				}
			}
		}
	}
}

func buildRefEdges(config PlanConfiguration) map[string][]string {
	edges := map[string][]string{}
	outputMap := buildModuleOutputMap(config)
	collectFromModule(config.RootModule, "", nil, edges, outputMap)
	return edges
}

func collectFromModule(mod ConfigModule, modulePrefix string, varMap map[string]string, edges map[string][]string, outputMap map[string]string) {
	for _, res := range mod.Resources {
		srcAddr := res.Address
		if modulePrefix != "" {
			srcAddr = modulePrefix + "." + res.Address
		}

		rawRefs := extractReferences(res.Expressions)

		seen := map[string]bool{}
		for _, ref := range rawRefs {
			resolved := resolveRef(ref, modulePrefix, varMap)
			if resolved == "" || resolved == srcAddr || seen[resolved] {
				continue
			}
			seen[resolved] = true
			edges[srcAddr] = append(edges[srcAddr], resolved)
		}
	}

	for callName, call := range mod.ModuleCalls {
		childPrefix := "module." + callName
		if modulePrefix != "" {
			childPrefix = modulePrefix + ".module." + callName
		}
		childVarMap := buildVarMap(call.Expressions, outputMap)
		propagateParentVars(call.Expressions, varMap, childVarMap)
		collectFromModule(call.Module, childPrefix, childVarMap, edges, outputMap)
	}
}

type containmentKeyDef struct {
	childTypes []string
	priority   int
}

var containmentKeys = map[string]containmentKeyDef{
	"subnet_id":  {priority: 50},
	"subnet_ids": {priority: 50},
	"subnets":    {priority: 50},
	"vpc_id":     {priority: 100},

	"route_table_id":    {childTypes: []string{"aws_route", "aws_route_table_association"}, priority: 30},
	"security_group_id": {childTypes: []string{"aws_security_group_rule", "aws_vpc_security_group_"}, priority: 30},
	"network_acl_id":    {childTypes: []string{"aws_network_acl_rule"}, priority: 30},

	"load_balancer_arn": {childTypes: []string{"aws_lb_listener", "aws_alb_listener", "aws_lb_target_group"}, priority: 30},
	"listener_arn":      {childTypes: []string{"aws_lb_listener_rule", "aws_alb_listener_rule", "aws_lb_listener_certificate"}, priority: 20},
	"target_group_arn":  {childTypes: []string{"aws_lb_target_group_attachment", "aws_alb_target_group_attachment"}, priority: 30},

	"cluster": {childTypes: []string{"aws_ecs_service", "aws_ecs_task"}, priority: 30},

	"application": {childTypes: []string{"aws_elastic_beanstalk_environment", "aws_elastic_beanstalk_application_version"}, priority: 30},

	"bucket": {childTypes: []string{"aws_s3_bucket_", "aws_s3_object"}, priority: 30},

	"role": {childTypes: []string{"aws_iam_role_policy", "aws_iam_instance_profile"}, priority: 30},

	"rest_api_id": {childTypes: []string{"aws_api_gateway_"}, priority: 30},

	"function_name": {childTypes: []string{"aws_lambda_permission", "aws_lambda_alias", "aws_lambda_event_source_mapping", "aws_lambda_function_url", "aws_lambda_provisioned_concurrency"}, priority: 30},

	"log_group_name": {childTypes: []string{"aws_cloudwatch_log_stream", "aws_cloudwatch_log_metric_filter", "aws_cloudwatch_log_subscription_filter"}, priority: 30},

	"repository": {childTypes: []string{"aws_ecr_lifecycle_policy", "aws_ecr_repository_policy"}, priority: 30},

	"cluster_identifier":    {childTypes: []string{"aws_rds_cluster_instance", "aws_rds_cluster_endpoint"}, priority: 30},
	"db_subnet_group_name":  {childTypes: []string{"aws_db_instance", "aws_rds_cluster"}, priority: 40},
	"db_instance_identifier": {childTypes: []string{"aws_db_instance_role_association", "aws_db_snapshot"}, priority: 30},

	"replication_group_id": {childTypes: []string{"aws_elasticache_cluster"}, priority: 30},

	"autoscaling_group_name": {childTypes: []string{"aws_autoscaling_attachment", "aws_autoscaling_policy", "aws_autoscaling_schedule", "aws_autoscaling_lifecycle_hook"}, priority: 30},
	"launch_template":        {childTypes: []string{"aws_autoscaling_group"}, priority: 40},

	"user_pool_id": {childTypes: []string{"aws_cognito_user_pool_client", "aws_cognito_user_group", "aws_cognito_resource_server", "aws_cognito_identity_provider", "aws_cognito_user_pool_domain"}, priority: 30},

	"topic_arn": {childTypes: []string{"aws_sns_topic_subscription", "aws_sns_topic_policy"}, priority: 30},
	"queue_url": {childTypes: []string{"aws_sqs_queue_policy", "aws_sqs_queue_redrive_policy"}, priority: 30},

	"target_key_id": {childTypes: []string{"aws_kms_alias"}, priority: 30},
	"key_id":        {childTypes: []string{"aws_kms_grant"}, priority: 30},

	"table_name": {childTypes: []string{"aws_dynamodb_table_item", "aws_dynamodb_tag", "aws_dynamodb_kinesis_streaming_destination"}, priority: 30},

	"distribution_id": {childTypes: []string{"aws_cloudfront_monitoring_subscription", "aws_cloudfront_origin_access_identity"}, priority: 30},

	"domain_name": {childTypes: []string{"aws_elasticsearch_domain_policy", "aws_opensearch_domain_policy", "aws_opensearch_domain_saml_options"}, priority: 30},

	"certificate_arn": {childTypes: []string{"aws_acm_certificate_validation", "aws_lb_listener_certificate"}, priority: 30},

	"cluster_name": {childTypes: []string{"aws_eks_node_group", "aws_eks_addon", "aws_eks_fargate_profile", "aws_eks_identity_provider_config"}, priority: 30},

	"project_name": {childTypes: []string{"aws_codebuild_webhook"}, priority: 30},

	"secret_id": {childTypes: []string{"aws_secretsmanager_secret_version", "aws_secretsmanager_secret_policy", "aws_secretsmanager_secret_rotation"}, priority: 30},

	"web_acl_id": {childTypes: []string{"aws_waf_web_acl_association", "aws_wafv2_web_acl_association", "aws_wafv2_web_acl_logging_configuration"}, priority: 30},

	"state_machine_arn": {childTypes: []string{"aws_sfn_alias"}, priority: 30},

	"event_bus_name": {childTypes: []string{"aws_cloudwatch_event_rule", "aws_cloudwatch_event_archive"}, priority: 30},
	"rule":           {childTypes: []string{"aws_cloudwatch_event_target"}, priority: 20},

	"stream_name": {childTypes: []string{"aws_kinesis_firehose_delivery_stream"}, priority: 30},

	"zone_id": {childTypes: []string{"aws_route53_record"}, priority: 30},
}

func matchesChildType(resType string, patterns []string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if strings.HasPrefix(resType, p) {
			return true
		}
	}
	return false
}

func buildContainmentMap(config PlanConfiguration) map[string]string {
	parentMap := map[string]string{}
	outputMap := buildModuleOutputMap(config)
	collectContainment(config.RootModule, "", nil, parentMap, outputMap)
	return parentMap
}

func collectContainment(mod ConfigModule, modulePrefix string, varMap map[string]string, parentMap map[string]string, outputMap map[string]string) {
	for _, res := range mod.Resources {
		srcAddr := res.Address
		if modulePrefix != "" {
			srcAddr = modulePrefix + "." + res.Address
		}

		parent := findBestContainmentParent(res.Type, res.Expressions, modulePrefix, varMap)
		if parent != "" && parent != srcAddr {
			parentMap[srcAddr] = parent
		}
	}

	for callName, call := range mod.ModuleCalls {
		childPrefix := "module." + callName
		if modulePrefix != "" {
			childPrefix = modulePrefix + ".module." + callName
		}
		childVarMap := buildVarMap(call.Expressions, outputMap)
		propagateParentVars(call.Expressions, varMap, childVarMap)
		collectContainment(call.Module, childPrefix, childVarMap, parentMap, outputMap)
	}
}

func findBestContainmentParent(resType string, exprs interface{}, modulePrefix string, varMap map[string]string) string {
	bestParent := ""
	bestPriority := 999
	scanContainmentExpr(exprs, resType, modulePrefix, varMap, &bestParent, &bestPriority)
	return bestParent
}

func scanContainmentExpr(v interface{}, resType, modulePrefix string, varMap map[string]string, bestParent *string, bestPriority *int) {
	switch val := v.(type) {
	case map[string]interface{}:
		for key, child := range val {
			if def, ok := containmentKeys[key]; ok && def.priority < *bestPriority && matchesChildType(resType, def.childTypes) {
				refs := getDirectRefs(child)
				for _, ref := range refs {
					resolved := resolveRef(ref, modulePrefix, varMap)
					if resolved != "" && !isModuleOnlyPrefix(resolved) {
						*bestParent = resolved
						*bestPriority = def.priority
						break
					}
				}
			}
			scanContainmentExpr(child, resType, modulePrefix, varMap, bestParent, bestPriority)
		}
	case []interface{}:
		for _, child := range val {
			scanContainmentExpr(child, resType, modulePrefix, varMap, bestParent, bestPriority)
		}
	}
}

func getDirectRefs(v interface{}) []string {
	if m, ok := v.(map[string]interface{}); ok {
		if refList, ok := m["references"]; ok {
			if arr, ok := refList.([]interface{}); ok {
				var refs []string
				for _, item := range arr {
					if s, ok := item.(string); ok {
						refs = append(refs, s)
					}
				}
				return refs
			}
		}
	}
	return nil
}

func collectAllPlannedResources(mod Module) []Resource {
	var all []Resource
	all = append(all, mod.Resources...)
	for _, child := range mod.ChildModules {
		all = append(all, collectAllPlannedResources(child)...)
	}
	return all
}

func enrichContainmentFromValues(planned []Resource, containment map[string]string) {
	idToAddr := map[string]string{}
	for _, res := range planned {
		if id, ok := res.Values["id"].(string); ok && id != "" {
			idToAddr[id] = stripIndex(res.Address)
		}
	}

	for _, res := range planned {
		addr := stripIndex(res.Address)
		if _, hasParent := containment[addr]; hasParent {
			continue
		}
		parent := findValueBasedParent(res.Values, idToAddr)
		if parent != "" && parent != addr {
			containment[addr] = parent
		}
	}
}

func findValueBasedParent(values map[string]interface{}, idToAddr map[string]string) string {
	if sid, ok := values["subnet_id"].(string); ok && sid != "" {
		if addr, ok := idToAddr[sid]; ok {
			return addr
		}
	}
	if parent := resolveFirstID(values, "subnet_ids", idToAddr); parent != "" {
		return parent
	}
	if vpcConfigs, ok := values["vpc_config"].([]interface{}); ok && len(vpcConfigs) > 0 {
		if config, ok := vpcConfigs[0].(map[string]interface{}); ok {
			if parent := resolveFirstID(config, "subnet_ids", idToAddr); parent != "" {
				return parent
			}
			if vid, ok := config["vpc_id"].(string); ok && vid != "" {
				if addr, ok := idToAddr[vid]; ok {
					return addr
				}
			}
		}
	}
	if vid, ok := values["vpc_id"].(string); ok && vid != "" {
		if addr, ok := idToAddr[vid]; ok {
			return addr
		}
	}
	return ""
}

func resolveFirstID(values map[string]interface{}, key string, idToAddr map[string]string) string {
	ids, ok := values[key].([]interface{})
	if !ok {
		return ""
	}
	for _, id := range ids {
		if sid, ok := id.(string); ok && sid != "" {
			if addr, ok := idToAddr[sid]; ok {
				return addr
			}
		}
	}
	return ""
}

func buildPlannedValuesMap(planned []Resource) map[string]map[string]interface{} {
	m := map[string]map[string]interface{}{}
	for _, res := range planned {
		m[stripIndex(res.Address)] = res.Values
	}
	return m
}

func enrichNetworkLabel(label, resType string, values map[string]interface{}) string {
	if values == nil {
		return label
	}
	var extras []string
	if nameTag := getNameTag(values); nameTag != "" {
		extras = append(extras, nameTag)
	}
	if cidr, ok := values["cidr_block"].(string); ok && cidr != "" {
		extras = append(extras, cidr)
	}
	if resType == "aws_subnet" {
		if az, ok := values["availability_zone"].(string); ok && az != "" {
			extras = append(extras, az)
		}
	}
	if len(extras) > 0 {
		label += "\n" + strings.Join(extras, " | ")
	}
	return label
}

func getNameTag(values map[string]interface{}) string {
	tags, ok := values["tags"].(map[string]interface{})
	if !ok {
		return ""
	}
	name, _ := tags["Name"].(string)
	return name
}

func buildGraphJSON(analyzed AnalyzedPlan, refEdges map[string][]string, containment map[string]string, plannedValues map[string]map[string]interface{}) (string, string, error) {
	type elem struct {
		Data    map[string]interface{} `json:"data"`
		Classes string                 `json:"classes,omitempty"`
	}

	elements := make([]elem, 0)
	resourceDetails := map[string]ResourceAnalysis{}
	knownNodes := map[string]bool{}

	for _, m := range analyzed.Modules {
		for _, r := range m.Resources {
			rID := r.Address
			label := r.Name + "\n(" + r.Type + ")"
			if r.Type == "aws_vpc" || r.Type == "aws_subnet" {
				label = enrichNetworkLabel(label, r.Type, plannedValues[stripIndex(rID)])
			}
			classes := "resource"
			if r.Action != "" {
				classes += " " + r.Action
			}

			nodeData := map[string]interface{}{
				"id":     rID,
				"label":  label,
				"type":   r.Type,
				"action": r.Action,
				"module": m.Address,
			}

			if parent, ok := containment[stripIndex(rID)]; ok {
				nodeData["parent"] = parent
			}

			elements = append(elements, elem{Data: nodeData, Classes: classes})
			knownNodes[rID] = true
			resourceDetails[r.Address] = r
		}
	}

	for i := range elements {
		parentID, hasParent := elements[i].Data["parent"]
		if !hasParent {
			continue
		}
		parentStr, _ := parentID.(string)
		if knownNodes[parentStr] {
			continue
		}
		cur := parentStr
		visited := map[string]bool{cur: true}
		found := false
		for {
			gp, ok := containment[cur]
			if !ok || visited[gp] {
				break
			}
			visited[gp] = true
			if knownNodes[gp] {
				elements[i].Data["parent"] = gp
				found = true
				break
			}
			cur = gp
		}
		if !found {
			delete(elements[i].Data, "parent")
		}
	}

	knownBase := map[string]bool{}
	for id := range knownNodes {
		knownBase[stripIndex(id)] = true
	}

	added := true
	for added {
		added = false
		for child, parent := range containment {
			if !knownNodes[child] && !knownBase[child] {
				continue
			}
			if knownNodes[parent] {
				continue
			}
			if knownBase[parent] {
				continue
			}
			parts := strings.Split(parent, ".")
			resType := ""
			resName := ""
			for i, p := range parts {
				if strings.Contains(p, "_") && i+1 < len(parts) {
					resType = p
					resName = parts[i+1]
					break
				}
			}
			label := parent
			if resType != "" {
				label = resName + "\n(" + resType + ")"
			} else if len(parts) >= 2 {
				label = parts[len(parts)-1] + "\n(" + parts[len(parts)-2] + ")"
			}
			if resType == "aws_vpc" || resType == "aws_subnet" {
				label = enrichNetworkLabel(label, resType, plannedValues[parent])
			}
			nodeData := map[string]interface{}{
				"id":    parent,
				"label": label,
				"type":  resType,
			}
			if pp, ok := containment[parent]; ok {
				nodeData["parent"] = pp
			}
			elements = append(elements, elem{Data: nodeData, Classes: "resource container"})
			knownNodes[parent] = true
			added = true
		}
	}

	containmentPairs := map[string]bool{}
	for child, parent := range containment {
		containmentPairs[child+"->"+parent] = true
	}

	for src, targets := range refEdges {
		for _, tgt := range targets {
			if !knownNodes[src] || !knownNodes[tgt] {
				continue
			}
			if containmentPairs[stripIndex(src)+"->"+stripIndex(tgt)] {
				continue
			}
			edgeID := "edge:ref:" + src + "->" + tgt
			elements = append(elements, elem{
				Data:    map[string]interface{}{"id": edgeID, "source": src, "target": tgt},
				Classes: "reference",
			})
		}
	}

	for _, m := range analyzed.Modules {
		for _, r := range m.Resources {
			for _, dep := range r.DependsOn {
				if !knownNodes[dep] {
					continue
				}
				edgeID := "edge:dep:" + dep + "->" + r.Address
				elements = append(elements, elem{
					Data:    map[string]interface{}{"id": edgeID, "source": dep, "target": r.Address},
					Classes: "depends_on",
				})
			}
		}
	}

	elJSON, err := json.Marshal(elements)
	if err != nil {
		return "", "", err
	}
	rdJSON, err := json.Marshal(resourceDetails)
	if err != nil {
		return "", "", err
	}
	return string(elJSON), string(rdJSON), nil
}

func ifReplaceComment(isReplace bool) string {
	if isReplace {
		return " # forces replacement"
	}
	return ""
}

func uniqueSortedKeys(maps ...map[string]interface{}) []string {
	keysMap := make(map[string]struct{})
	for _, m := range maps {
		for k := range m {
			keysMap[k] = struct{}{}
		}
	}
	var keys []string
	for k := range keysMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func formatValue(v interface{}) string {
	if v == nil {
		return "null"
	}

	switch val := v.(type) {
	case string:
		return fmt.Sprintf("%q", val)
	case float64:
		return fmt.Sprintf("%g", val)
	case bool:
		return fmt.Sprintf("%t", val)
	case map[string]interface{}, []interface{}:
		out, err := json.MarshalIndent(val, "", "  ")
		if err == nil {
			return string(out)
		}
		return fmt.Sprintf("%v", v)
	}

	return fmt.Sprintf("%v", v)
}

func formatJSON(s string) (string, bool) {
	var js interface{}
	if err := json.Unmarshal([]byte(s), &js); err != nil {
		return s, false
	}
	out, err := json.MarshalIndent(js, "", "  ")
	if err != nil {
		return s, false
	}
	return string(out), true
}

func generateHTML(analysis AnalyzedPlan, showGraph bool, refEdges map[string][]string, containment map[string]string, plannedValues map[string]map[string]interface{}) string {
	graphJSON, resourceDetailsJSON, _ := buildGraphJSON(analysis, refEdges, containment, plannedValues)
	data := struct {
		AnalyzedPlan
		GraphJSON           template.JS
		ResourceDetailsJSON template.JS
		ShowGraph           bool
	}{
		AnalyzedPlan:        analysis,
		GraphJSON:           template.JS(graphJSON),
		ResourceDetailsJSON: template.JS(resourceDetailsJSON),
		ShowGraph:           showGraph,
	}

	htmlTemplate := `<!DOCTYPE html>

<html lang="ko">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Terraform Plan Analysis</title>
  <script src="https://cdnjs.cloudflare.com/ajax/libs/cytoscape/3.28.1/cytoscape.min.js"></script>
  <script src="https://cdn.jsdelivr.net/npm/elkjs@0.9.3/lib/elk.bundled.js"></script>
  <script src="https://cdn.jsdelivr.net/npm/cytoscape-elk@2.2.0/dist/cytoscape-elk.js"></script>
  <style>
    :root {
      --background-color: #f7f8fa;
      --container-bg: #ffffff;
      --sidebar-bg: #f1f3f6;
      --border-color: #e1e4e8;
      --text-color: #24292e;
      --text-secondary-color: #586069;
      --accent-color: #0366d6;
      --create-color: #28a745;
      --update-color: #dbab09;
      --delete-color: #d73a49;
      --font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
    }
    * {
      box-sizing: border-box;
      margin: 0;
      padding: 0;
    }
    body {
      font-family: var(--font-family);
      background-color: var(--background-color);
      color: var(--text-color);
      font-size: 14px;
    }
    .container {
      max-width: 1200px;
      margin: 20px auto;
      background: var(--container-bg);
      border-radius: 8px;
      border: 1px solid var(--border-color);
      overflow: hidden;
    }
    .header {
      padding: 20px;
      border-bottom: 1px solid var(--border-color);
    }
    .header h1 {
      font-size: 24px;
      margin-bottom: 5px;
    }
    .header .subtitle {
      font-size: 12px;
      color: var(--text-secondary-color);
    }
    .search-container {
      margin-top: 15px;
    }
    .search-container input {
      width: 100%;
      padding: 10px;
      border: 1px solid var(--border-color);
      border-radius: 4px;
      font-size: 14px;
    }
    .summary {
      padding: 20px;
      border-bottom: 1px solid var(--border-color);
      display: flex;
      gap: 20px;
    }
    .summary-item {
      text-align: center;
    }
    .summary-item h2 {
      font-size: 28px;
    }
    .summary-item p {
      font-size: 12px;
      color: var(--text-secondary-color);
    }
    .filters {
      display: flex;
      gap: 10px;
      margin-top: 15px;
      justify-content: center;
    }
    .filter-btn {
      background-color: #fff;
      border: 1px solid var(--border-color);
      padding: 8px 12px;
      border-radius: 4px;
      cursor: pointer;
      font-size: 12px;
      transition: background-color 0.2s, color 0.2s;
    }
    .filter-btn:hover {
      background-color: #f0f0f0;
    }
    .filter-btn.active {
      background-color: var(--accent-color);
      color: white;
      border-color: var(--accent-color);
    }
    #graph {
      width: 100%;
      height: 700px;
      border: 1px solid var(--border-color);
      margin-top: 20px;
      background: #fafbfc;
    }
    .graph-toolbar {
      display: flex;
      justify-content: space-between;
      align-items: flex-start;
      padding: 10px 20px;
      gap: 10px;
      border-bottom: 1px solid var(--border-color);
      flex-wrap: wrap;
      background: var(--sidebar-bg);
    }
    .graph-toolbar-left {
      display: flex;
      gap: 16px;
      align-items: flex-start;
      flex-wrap: wrap;
      flex: 1;
    }
    .toolbar-group {
      display: flex;
      gap: 6px;
      align-items: center;
      flex-wrap: wrap;
    }
    .toolbar-label {
      font-size: 11px;
      font-weight: 600;
      color: var(--text-secondary-color);
      white-space: nowrap;
    }
    .mod-btn {
      padding: 3px 10px;
      border: 1px solid var(--border-color);
      border-radius: 12px;
      background: #fff;
      cursor: pointer;
      font-size: 11px;
      transition: all 0.15s;
    }
    .mod-btn.active {
      background: var(--accent-color);
      color: white;
      border-color: var(--accent-color);
    }
    .mod-btn:hover { opacity: 0.8; }
    .ctrl-btn {
      padding: 3px 10px;
      border: 1px solid var(--border-color);
      border-radius: 4px;
      background: #fff;
      cursor: pointer;
      font-size: 11px;
    }
    .ctrl-btn:hover { background: #f0f0f0; }
    .graph-legend {
      display: flex;
      gap: 12px;
      align-items: center;
    }
    .legend-item {
      display: flex;
      align-items: center;
      gap: 4px;
      font-size: 11px;
      color: var(--text-secondary-color);
    }
    .legend-swatch {
      width: 20px;
      height: 10px;
      border-radius: 2px;
    }
    .module {
      border-bottom: 1px solid var(--border-color);
    }
    .module:last-child {
      border-bottom: none;
    }
    .module-header {
      background: var(--sidebar-bg);
      padding: 10px 20px;
      font-size: 16px;
      font-weight: 600;
    }
    .resource {
      padding: 15px 20px;
      border-bottom: 1px solid var(--border-color);
      cursor: pointer;
    }
    .resource:last-child {
      border-bottom: none;
    }
    .resource-header {
      display: flex;
      align-items: center;
      gap: 10px;
    }
    .action-icon {
      width: 20px;
      height: 20px;
      border-radius: 50%;
      color: white;
      text-align: center;
      line-height: 20px;
      font-weight: bold;
      text-transform: uppercase;
    }
    .action-icon.create { background-color: var(--create-color); }
    .action-icon.update { background-color: var(--update-color); }
    .action-icon.delete { background-color: var(--delete-color); }
    .resource.resource-changed-create { border-left: 4px solid var(--create-color); }
    .resource.resource-changed-update { border-left: 4px solid var(--update-color); }
    .resource.resource-changed-delete { border-left: 4px solid var(--delete-color); }
    .resource-info h3 {
      font-size: 16px;
    }
    .resource-info p {
      font-size: 12px;
      color: var(--text-secondary-color);
    }
    .details {
      margin-top: 15px;
      padding-left: 30px;
      display: none;
    }
    .details pre {
      background: #f6f8fa;
      border: 1px solid var(--border-color);
      border-radius: 6px;
      padding: 15px;
      white-space: pre-wrap;
      word-break: break-all;
      font-family: "SFMono-Regular", Consolas, "Liberation Mono", Menlo, Courier, monospace;
      font-size: 12px;
    }
    .diff-line-added {
      background-color: #e6ffed;
    }
    .diff-line-removed {
      background-color: #ffeef0;
    }
    .diff-line-modified {
      background-color: #fffab8;
    }
    .diff-line-unchanged {
      color: var(--text-secondary-color);
    }
  </style>
</head>
<body>
  <div class="container">
    <div class="header">
      <h1>Terraform Plan</h1>
      <div class="subtitle">{{.Timestamp}} (v{{.TerraformVersion}})</div>
      <div class="search-container">
        <input type="text" id="resourceSearch" placeholder="Search resources..." onkeyup="filterResources()">
      </div>
    </div>
    <div class="summary">
      <div class="summary-item">
        <h2>{{.Summary.TotalResources}}</h2>
        <p>Total</p>
      </div>
      <div class="summary-item">
        <h2 style="color: var(--create-color)">{{index .Summary.Actions "create"}}</h2>
        <p>Create</p>
      </div>
      <div class="summary-item">
        <h2 style="color: var(--update-color)">{{index .Summary.Actions "update"}}</h2>
        <p>Update</p>
      </div>
      <div class="summary-item">
        <h2 style="color: var(--delete-color)">{{index .Summary.Actions "delete"}}</h2>
        <p>Delete</p>
      </div>
      <div class="filters">
        <button class="filter-btn active" data-action="all" onclick="filterByAction('all', this)">All</button>
        <button class="filter-btn" data-action="create" onclick="filterByAction('create', this)">Create</button>
        <button class="filter-btn" data-action="update" onclick="filterByAction('update', this)">Update</button>
        <button class="filter-btn" data-action="delete" onclick="filterByAction('delete', this)">Delete</button>
        <button class="filter-btn" data-action="no-op" onclick="filterByAction('no-op', this)">No-op</button>
      </div>
    </div>

    {{if .ShowGraph}}
    <div class="graph-toolbar">
      <div class="graph-toolbar-left">
        <div class="toolbar-group">
          <span class="toolbar-label">Modules</span>
          <div id="moduleFilters"></div>
        </div>
        <div class="toolbar-group">
          <span class="toolbar-label">View</span>
          <button class="ctrl-btn" onclick="expandAll()">Expand All</button>
          <button class="ctrl-btn" onclick="collapseAll()">Collapse All</button>
          <button class="ctrl-btn" onclick="cy.fit(null,50)">Fit</button>
        </div>
      </div>
      <div class="graph-legend">
        <div class="legend-item"><div class="legend-swatch" style="background:#28a745;opacity:0.15;border:2px dashed #28a745"></div>VPC</div>
        <div class="legend-item"><div class="legend-swatch" style="background:#0366d6;opacity:0.15;border:2px dashed #0366d6"></div>Subnet</div>
        <div class="legend-item"><div class="legend-swatch" style="background:#28a745"></div>Create</div>
        <div class="legend-item"><div class="legend-swatch" style="background:#dbab09"></div>Update</div>
        <div class="legend-item"><div class="legend-swatch" style="background:#d73a49"></div>Delete</div>
        <div class="legend-item"><div class="legend-swatch" style="border-top:2px dashed #6f42c1;height:0"></div>Ref</div>
      </div>
    </div>
    <div id="graph"></div>
    {{end}}

    <div class="resource-list">
      {{range .Modules}}
      <div class="module">
        <div class="module-header">
          <h2>{{.Address}}</h2>
        </div>
        {{range .Resources}}
        <div class="resource{{if ne .Action "no-op"}} resource-changed-{{.Action}}{{end}}" onclick="toggleDetails(this)">
          <div class="resource-header">
            <div class="action-icon {{.Action}}">{{slice .Action 0 1}}</div>
            <div class="resource-info">
              <h3>{{.Address}}</h3>
              <p>{{.Type}}</p>
            </div>
          </div>
          <div class="details">
            <pre>{{range .DiffLines}}<div class="diff-line-{{.Type}}">{{.Text}}</div>{{end}}</pre>
            {{if .PolicyDocumentJSON}}
            <h4>Policy Document:</h4>
            <pre>{{.PolicyDocumentJSON}}</pre>
            {{end}}
          </div>
        </div>
        {{end}}
      </div>
      {{end}}
    </div>
  </div>

  {{if .ShowGraph}}
  <script>
    const elements = {{.GraphJSON}};
    const resourceDetails = {{.ResourceDetailsJSON}};

    const cy = cytoscape({
      container: document.getElementById('graph'),
      elements: elements,
      layout: {
        name: 'elk',
        elk: {
          algorithm: 'layered',
          'elk.direction': 'DOWN',
          'elk.spacing.nodeNode': '35',
          'elk.layered.spacing.nodeNodeBetweenLayers': '60',
          'elk.padding': '[top=50,left=30,bottom=30,right=30]',
          'elk.hierarchyHandling': 'INCLUDE_CHILDREN',
          'elk.layered.crossingMinimization.strategy': 'LAYER_SWEEP',
          'elk.layered.nodePlacement.strategy': 'BRANDES_KOEPF'
        },
        fit: true,
        padding: 50
      },
      style: [
        { selector: ':parent', style: {
            'label': 'data(label)',
            'text-valign': 'top',
            'text-halign': 'center',
            'text-margin-y': '10px',
            'font-size': '13px',
            'font-weight': 'bold',
            'color': '#333',
            'text-wrap': 'wrap',
            'text-max-width': '250px',
            'background-opacity': 0.07,
            'border-width': 2,
            'border-style': 'dashed',
            'padding': '30px',
            'shape': 'round-rectangle',
            'background-color': '#888',
            'border-color': '#888'
        }},
        { selector: ':parent[type = "aws_vpc"]', style: {
            'background-color': '#28a745',
            'border-color': '#28a745',
            'color': '#1a6d2e'
        }},
        { selector: ':parent[type = "aws_subnet"]', style: {
            'background-color': '#0366d6',
            'border-color': '#0366d6',
            'color': '#0550ae'
        }},
        { selector: '.cy-collapsed', style: {
            'background-opacity': 0.15,
            'border-style': 'solid'
        }},

        { selector: 'node:childless', style: {
            'label': 'data(label)',
            'width': 'label',
            'height': 'label',
            'padding': '12px',
            'text-valign': 'center',
            'text-halign': 'center',
            'color': '#fff',
            'text-outline-width': 2,
            'text-outline-color': '#555',
            'background-color': '#555',
            'shape': 'round-rectangle',
            'text-wrap': 'wrap',
            'text-max-width': '130px',
            'font-size': '10px',
            'border-width': 1,
            'border-color': '#fff',
            'border-opacity': 0.3
        }},
        { selector: 'node.create:childless', style: { 'background-color': '#28a745', 'text-outline-color': '#1a6d2e' }},
        { selector: 'node.update:childless', style: { 'background-color': '#dbab09', 'text-outline-color': '#8a6d00' }},
        { selector: 'node.delete:childless', style: { 'background-color': '#d73a49', 'text-outline-color': '#9e1c23' }},
        { selector: 'node.container:childless', style: { 'background-color': '#6a737d', 'text-outline-color': '#444d56' }},

        { selector: 'edge', style: {
            'width': 1.5,
            'line-color': '#bbb',
            'target-arrow-color': '#bbb',
            'target-arrow-shape': 'triangle',
            'curve-style': 'bezier',
            'opacity': 0.6
        }},
        { selector: 'edge.reference', style: {
            'line-color': '#6f42c1',
            'target-arrow-color': '#6f42c1',
            'line-style': 'dashed'
        }},
        { selector: 'edge.depends_on', style: {
            'line-color': '#e36209',
            'target-arrow-color': '#e36209',
            'line-style': 'dotted'
        }},
        { selector: '.faded', style: { 'opacity': 0.12 }},
        { selector: '.highlighted', style: { 'opacity': 1 }}
      ]
    });

    /* ── Collapse / Expand ── */
    function collapseNode(node) {
      const desc = node.descendants();
      const leafCount = desc.filter(':childless').length;
      node.data('_origLabel', node.data('label'));
      node.data('label', node.data('label').split('\n')[0] + '\n[' + leafCount + ' resources]');
      desc.addClass('cy-hidden');
      desc.style('display', 'none');
      desc.connectedEdges().style('display', 'none');
      node.addClass('cy-collapsed');
    }

    function expandNode(node) {
      if (!node.hasClass('cy-collapsed')) return;
      node.data('label', node.data('_origLabel') || node.data('label'));
      node.descendants().forEach(function(d) {
        if (!hiddenModules.has(d.data('module'))) {
          d.removeClass('cy-hidden');
          d.style('display', 'element');
        }
      });
      node.descendants().connectedEdges().forEach(function(e) {
        const s = e.source(), t = e.target();
        if (s.style('display') !== 'none' && t.style('display') !== 'none') {
          e.style('display', 'element');
        }
      });
      node.removeClass('cy-collapsed');
      node.descendants(':parent.cy-collapsed').forEach(function(child) {
        expandNode(child);
      });
    }

    cy.on('tap', ':parent', function(evt) {
      evt.stopPropagation();
      const node = evt.target;
      if (node.hasClass('cy-collapsed')) { expandNode(node); } else { collapseNode(node); }
    });

    function expandAll() {
      cy.nodes(':parent.cy-collapsed').forEach(function(n) { expandNode(n); });
    }
    function collapseAll() {
      cy.nodes(':parent').roots().forEach(function(n) { collapseNode(n); });
    }

    /* ── Module Filter ── */
    const hiddenModules = new Set();
    const moduleSet = new Set();
    elements.forEach(function(e) {
      if (e.data && e.data.module) moduleSet.add(e.data.module);
    });

    const mfDiv = document.getElementById('moduleFilters');
    const sorted = Array.from(moduleSet).sort();
    sorted.forEach(function(mod) {
      const btn = document.createElement('button');
      btn.className = 'mod-btn active';
      btn.textContent = mod;
      btn.onclick = function() {
        if (hiddenModules.has(mod)) {
          hiddenModules.delete(mod);
          btn.classList.add('active');
        } else {
          hiddenModules.add(mod);
          btn.classList.remove('active');
        }
        applyModuleFilter();
      };
      mfDiv.appendChild(btn);
    });

    function applyModuleFilter() {
      cy.nodes('[module]').forEach(function(node) {
        if (hiddenModules.has(node.data('module'))) {
          node.style('display', 'none');
          node.connectedEdges().style('display', 'none');
        } else {
          node.style('display', 'element');
        }
      });
      cy.edges().forEach(function(e) {
        const s = e.source(), t = e.target();
        if (s.style('display') !== 'none' && t.style('display') !== 'none') {
          e.style('display', 'element');
        } else {
          e.style('display', 'none');
        }
      });
      cy.nodes(':parent').forEach(function(p) {
        const vis = p.children().filter(function(c) { return c.style('display') !== 'none'; });
        if (vis.length === 0) { p.style('display', 'none'); } else { p.style('display', 'element'); }
      });
    }

    /* ── Highlight neighbors on leaf tap ── */
    cy.on('tap', 'node:childless', function(evt) {
      cy.elements().removeClass('faded highlighted');
      const n = evt.target;
      const hood = n.neighborhood().add(n);
      cy.elements().not(hood).not(':parent').addClass('faded');
      hood.addClass('highlighted');
    });
    cy.on('tap', function(evt) {
      if (evt.target === cy) cy.elements().removeClass('faded highlighted');
    });
  </script>
  {{end}}

  <script>
    function toggleDetails(el) {
      const details = el.querySelector('.details');
      if (details.style.display === 'block') {
        details.style.display = 'none';
      } else {
        details.style.display = 'block';
      }
    }

    function filterResources() {
      const input = document.getElementById('resourceSearch');
      const filterText = input.value.toLowerCase();
      const activeFilterButton = document.querySelector('.filter-btn.active');
      const filterAction = activeFilterButton ? activeFilterButton.dataset.action : 'all';

      const modules = document.querySelectorAll('.module');

      modules.forEach(module => {
        let moduleHasVisibleResources = false;
        const resources = module.querySelectorAll('.resource');
        resources.forEach(resource => {
          const address = resource.querySelector('h3').textContent.toLowerCase();
          const type = resource.querySelector('p').textContent.toLowerCase();
          const action = resource.querySelector('.action-icon').classList[1];

          const matchesSearch = address.includes(filterText) || type.includes(filterText) || action.includes(filterText);
          const matchesAction = filterAction === 'all' || action === filterAction;

          if (matchesSearch && matchesAction) {
            resource.style.display = '';
            moduleHasVisibleResources = true;
          } else {
            resource.style.display = 'none';
          }
        });

        if (moduleHasVisibleResources) {
          module.style.display = '';
        } else {
          module.style.display = 'none';
        }
      });
    }

    function filterByAction(action, clickedButton) {
      const filterButtons = document.querySelectorAll('.filter-btn');
      filterButtons.forEach(btn => btn.classList.remove('active'));
      clickedButton.classList.add('active');
      filterResources();
    }
  </script>
</body>
</html>`

	tmpl, err := template.New("html").Parse(htmlTemplate)
	if err != nil {
		fmt.Printf("❌ Error parsing HTML template: %v\n", err)
		return "<html><body>Error parsing template</body></html>"
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, data)
	if err != nil {
		fmt.Printf("❌ Error executing template: %v\n", err)
		return "<html><body>Error rendering template</body></html>"
	}

	return buf.String()
}
