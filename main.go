package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

type TerraformPlan struct {
	FormatVersion    string           `json:"format_version"`
	TerraformVersion string           `json:"terraform_version"`
	PlannedValues    PlannedValues    `json:"planned_values"`
	ResourceChanges  []ResourceChange `json:"resource_changes"`
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
	html := generateHTML(analyzed, showGraph)

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
	html := generateHTML(analyzed, showGraph)
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
        		action = "update" // replace는 update로 처리
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

		// Check for policy documents and pretty-print them
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

	// Resource header line
	lines = append(lines, DiffLine{Type: "header", Text: fmt.Sprintf("%s resource \"%s\" \"%s\" {", actionPrefix, rc.Type, rc.Name)})

	// Generate diff for attributes
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
				// Value is unknown after apply and is a true boolean
				if bOk {
					*lines = append(*lines, DiffLine{Type: "modified", Text: fmt.Sprintf("%s  %s = %s => (known after apply)%s", indent, key, formatValue(bv), comment)})
				} else {
					*lines = append(*lines, DiffLine{Type: "added", Text: fmt.Sprintf("%s+ %s = (known after apply)%s", indent, key, comment)})
				}
				continue // Processed this key, move to next
			}
			// If auv is not a bool, or is false, fall through to other change types
		}

		if bOk && !aOk {
			// Removed attribute
			*lines = append(*lines, DiffLine{Type: "removed", Text: fmt.Sprintf("%s- %s = %s%s", indent, key, formatValue(bv), comment)})
		} else if !bOk && aOk {
			// Added attribute
			*lines = append(*lines, DiffLine{Type: "added", Text: fmt.Sprintf("%s+ %s = %s%s", indent, key, formatValue(av), comment)})
		} else if bOk && aOk && !deepEqual(bv, av) {
			// Modified attribute
			// Handle nested structures recursively
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
			// Unchanged attribute
			*lines = append(*lines, DiffLine{Type: "unchanged", Text: fmt.Sprintf("%s  %s = %s", indent, key, formatValue(av))})
		}
	}
}

func buildGraphJSON(analyzed AnalyzedPlan) (string, string, error) {
	type elem struct {
		Data    map[string]interface{} `json:"data"`
		Classes string                 `json:"classes,omitempty"`
	}

	elements := make([]elem, 0)
	resourceDetails := map[string]ResourceAnalysis{}

	// add module nodes and resource nodes + edges
	for _, m := range analyzed.Modules {
		modID := "mod:" + m.Address
		elements = append(elements, elem{
			Data:    map[string]interface{}{"id": modID, "label": m.Address, "group": "module"},
			Classes: "module",
		})

		for _, r := range m.Resources {
			// resource node
			rID := r.Address
			label := r.Address
			if r.Name != "" {
				label = r.Name + " (" + r.Type + ")"
			}
			classes := "resource"
			if r.Action != "" {
				classes = classes + " " + r.Action
			}

			elements = append(elements, elem{
				Data:    map[string]interface{}{"id": rID, "label": label, "type": r.Type, "action": r.Action},
				Classes: classes,
			})

			// edge: module -> resource (containment)
			eID := "edge:contains:" + modID + ":" + rID
			elements = append(elements, elem{
				Data: map[string]interface{}{"id": eID, "source": modID, "target": rID, "label": "contains"},
			})

			// add depends_on edges (resource -> resource)
			for _, dep := range r.DependsOn {
				// dep might be like "aws_instance.foo"; only add edge if dep exists as node later (cytoscape tolerates missing nodes)
				edgeID := "edge:dep:" + dep + "->" + rID
				elements = append(elements, elem{
					Data: map[string]interface{}{"id": edgeID, "source": dep, "target": rID, "label": "depends_on"},
				})
			}

			resourceDetails[r.Address] = r
		}
	}

	// produce JSON strings
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
		return fmt.Sprintf("%v", v) // Fallback if JSON marshaling fails
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

func generateHTML(analysis AnalyzedPlan, showGraph bool) string {
	graphJSON, resourceDetailsJSON, _ := buildGraphJSON(analysis)
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
  <script src="https://unpkg.com/dagre@0.8.5/dist/dagre.min.js"></script>
  <script src="https://unpkg.com/cytoscape-dagre@2.5.0/cytoscape-dagre.js"></script>
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
      height: 500px;
      border: 1px solid var(--border-color);
      margin-top: 20px;
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
    const cy = cytoscape({
      container: document.getElementById('graph'),
      elements: elements,
      layout: { 
        name: 'dagre',
        rankDir: 'TB',
        spacingFactor: 1.2,
        nodeSep: 40, 
        edgeSep: 20,
        rankSep: 50,
       },
      style: [
        { selector: 'node', style: {
            'label': 'data(label)',
			'width': 'label',    
            'height': 'label',    
            'padding': '12px',    
            'text-valign': 'center',
            'color': '#fff',
            'text-outline-width': 2,
            'text-outline-color': '#555',
            'background-color': '#555',
            'text-wrap': 'wrap',
            'text-max-width': '80px'
        }},
        { selector: 'node.create', style: { 'background-color': '#28a745' }},
        { selector: 'node.update', style: { 'background-color': '#dbab09' }},
        { selector: 'node.delete', style: { 'background-color': '#d73a49' }},
        { selector: 'node.module', style: { 'background-color': '#0366d6', 'shape': 'round-rectangle' }},
        { selector: 'edge', style: {
            'width': 2,
            'line-color': '#ccc',
            'target-arrow-color': '#ccc',
            'target-arrow-shape': 'triangle',
            'curve-style': 'bezier'
        }}
      ]
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
          const action = resource.querySelector('.action-icon').classList[1]; // e.g., "create", "update"

          const matchesSearch = address.includes(filterText) || type.includes(filterText) || action.includes(filterText);
          const matchesAction = filterAction === 'all' || action === filterAction;

          if (matchesSearch && matchesAction) {
            resource.style.display = '';
            moduleHasVisibleResources = true;
          } else {
            resource.style.display = 'none';
          }
        });

        // Show/hide module header based on whether it has visible resources
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
      filterResources(); // Re-run filter with new action
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
