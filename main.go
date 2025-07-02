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

type ResourceAnalysis struct {
	Address     string         `json:"address"`
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Provider    string         `json:"provider"`
	Action      string         `json:"action"`
	Changes     []ChangeDetail `json:"changes,omitempty"`
	Impact      string         `json:"impact"`
	Description string         `json:"description"`
	BeforeJSON  template.HTML  `json:"before_json"`
	AfterJSON   template.HTML  `json:"after_json"`
	DiffText    template.HTML  `json:"diff_text"`
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
	output, err := showCmd.Output()
	if err != nil {
		fmt.Printf("❌ Error running terraform show: %v\n", err)
		os.Exit(1)
	}

	var plan TerraformPlan
	err = json.Unmarshal(output, &plan)
	if err != nil {
		fmt.Printf("❌ Error parsing JSON plan: %v\n", err)
		os.Exit(1)
	}

	analyzed := analyzePlan(plan)
	html := generateHTML(analyzed)

	err = os.Remove(planBinaryFile)
	if err != nil {
		fmt.Printf("❌ Error deleting plan file %s: %v\n", planBinaryFile, err)
	} else {
		fmt.Println("✅ Plan file deleted successfully")
	}

	serveHTMLOnce(html)
}

func generateHTMLFromJSON(planFile string) {
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
	filename := generateHTML(analyzed)
	serveHTMLOnce(filename)
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
			action = rc.Change.Actions[0]
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
		}

		res.Changes = analyzeChanges(rc.Change.Before, rc.Change.After)

		isReplace := len(rc.Change.Actions) == 2 && rc.Change.Actions[0] == "delete" && rc.Change.Actions[1] == "create"
		diffStr := generateTerraformStyleDiff(rc.Change.Before, rc.Change.After, rc.Change.AfterUnknown, isReplace)
		res.DiffText = template.HTML(fmt.Sprintf("<pre>%s</pre>", template.HTMLEscapeString(diffStr)))

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

func generateTerraformStyleDiff(before, after, afterUnknown map[string]interface{}, isReplace bool) string {
	var sb strings.Builder
	allKeys := uniqueSortedKeys(before, after, afterUnknown)

	for _, key := range allKeys {
		bv, bOk := before[key]
		av, aOk := after[key]
		auOk := afterUnknown != nil && afterUnknown[key] != nil

		if auOk {
			if bOk {
				sb.WriteString(fmt.Sprintf("~ %s = %s -> (known after apply)%s\n", key, formatValue(bv), ifReplaceComment(isReplace)))
			} else {
				sb.WriteString(fmt.Sprintf("+ %s = (known after apply)%s\n", key, ifReplaceComment(isReplace)))
			}
		} else if bOk && !aOk {
			sb.WriteString(fmt.Sprintf("- %s = %s%s\n", key, formatValue(bv), ifReplaceComment(isReplace)))
		} else if !bOk && aOk {
			sb.WriteString(fmt.Sprintf("+ %s = %s%s\n", key, formatValue(av), ifReplaceComment(isReplace)))
		} else if bOk && aOk && !deepEqual(bv, av) {
			sb.WriteString(fmt.Sprintf("~ %s = %s -> %s%s\n", key, formatValue(bv), formatValue(av), ifReplaceComment(isReplace)))
		}

	}

	return sb.String()
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
		return "<nil>"
	}

	switch val := v.(type) {
	case string:
		return fmt.Sprintf("%q", val)
	case map[string]interface{}, []interface{}:
		out, err := json.MarshalIndent(val, "", "  ")
		if err == nil {
			return string(out)
		}
	}

	return fmt.Sprintf("%v", v)
}

func generateHTML(analysis AnalyzedPlan) string {
	htmlTemplate := `<!DOCTYPE html>
<html lang="ko">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Terraform Plan Analysis</title>
  <style>
    * {
      margin: 0;
      padding: 0;
      box-sizing: border-box;
    }
    body {
      font-family: 'Segoe UI', Tahoma, Geneva, Verdana, sans-serif;
      background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
      min-height: 100vh;
      padding: 20px;
    }
    .container {
      max-width: 1200px;
      margin: 0 auto;
      background: rgba(255, 255, 255, 0.95);
      border-radius: 20px;
      backdrop-filter: blur(10px);
      box-shadow: 0 20px 40px rgba(0, 0, 0, 0.1);
      overflow: hidden;
    }
    .header {
      background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
      color: white;
      padding: 30px;
      text-align: center;
    }
    .header h1 {
      font-size: 2.5em;
      margin-bottom: 10px;
      font-weight: 300;
    }
    .header .subtitle {
      opacity: 0.9;
      font-size: 1.1em;
    }
    .summary {
      padding: 30px;
      border-bottom: 1px solid #eee;
    }
    .summary-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
      gap: 20px;
      margin-top: 20px;
    }
    .summary-card {
      background: #f8f9fa;
      padding: 20px;
      border-radius: 12px;
      text-align: center;
      border-left: 4px solid #667eea;
    }
    .summary-card h3 {
      color: #333;
      font-size: 2em;
      margin-bottom: 5px;
    }
    .summary-card p {
      color: #666;
      font-size: 0.9em;
    }
    .modules {
      padding: 30px;
    }
    .module {
      margin-bottom: 30px;
      border: 1px solid #e0e0e0;
      border-radius: 12px;
      overflow: hidden;
    }
    .module-header {
      background: #f8f9fa;
      padding: 20px;
      border-bottom: 1px solid #e0e0e0;
    }
    .module-title {
      font-size: 1.3em;
      color: #333;
      margin-bottom: 10px;
    }
    .module-info {
      display: flex;
      gap: 20px;
      flex-wrap: wrap;
    }
    .module-info span {
      background: #667eea;
      color: white;
      padding: 5px 12px;
      border-radius: 20px;
      font-size: 0.8em;
    }
    .resources {
      padding: 20px;
    }
    .resource {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: 15px;
      margin-bottom: 10px;
      background: #fff;
      border-radius: 8px;
      border-left: 4px solid #ddd;
      box-shadow: 0 2px 4px rgba(0, 0, 0, 0.05);
      cursor: pointer;
      overflow: visible; 
    }
    .resource.create {
      border-left-color: #28a745;
    }
    .resource.update {
      border-left-color: #ffc107;
    }
    .resource.delete {
      border-left-color: #dc3545;
    }
    .resource-info {
      flex: 1;
      min-width: 0;
    }
    .resource-info h4 {
      color: #333;
      margin-bottom: 5px;
    }
    .resource-info p {
      color: #666;
      font-size: 0.9em;
    }
    .resource-meta {
      text-align: right;
      min-width: 120px;
      flex-shrink: 0;
    }
    .action-badge {
      padding: 5px 12px;
      border-radius: 20px;
      font-size: 0.8em;
      font-weight: bold;
      text-transform: uppercase;
      user-select: none;
    }
    .action-create {
      background: #d4edda;
      color: #155724;
    }
    .action-update {
      background: #fff3cd;
      color: #856404;
    }
    .action-delete {
      background: #f8d7da;
      color: #721c24;
    }
    .action-no-op {
      background: #e2e3e5;
      color: #383d41;
    }
    .impact {
      margin-top: 5px;
      font-size: 0.8em;
      color: #666;
    }
    .details {
      background-color: #f0f0f0;
      border: 1px solid #ddd;
      border-radius: 6px;
      max-height: 300px;
      width: 100%;
      margin-top: 8px;
      padding: 0;
      display: none;
      overflow: auto;
      position: relative;
    }

    .details pre {
      font-family: 'Courier New', Consolas, monospace;
      font-size: 0.85em;
      white-space: pre;
      padding: 10px;
      margin: 0;
      min-width: max-content; 
      color: #333;
      line-height: 1.4;
    }
    
    @media (max-width: 768px) {
      .container {
        margin: 10px;
        border-radius: 12px;
      }
      .header {
        padding: 20px;
      }
      .header h1 {
        font-size: 2em;
      }
      .summary,
      .modules {
        padding: 20px;
      }
      .resource {
        flex-direction: column;
        align-items: flex-start;
        gap: 10px;
      }
      .resource-meta {
        text-align: left;
        min-width: auto;
      }
      .details {
        max-height: 200px; 
      }
    }
  </style>
</head>
<body>
  <div class="container">
    <div class="header">
      <h1>Terraform Plan Result Summary</h1>
      <div class="subtitle">{{.Timestamp}} (v{{.TerraformVersion}})</div>
    </div>

    <div class="summary">
      <h2>Summary</h2>
      <div class="summary-grid">
        <div class="summary-card">
          <h3>{{.Summary.TotalResources}}</h3>
          <p>Total Resources</p>
        </div>
        <div class="summary-card">
          <h3>{{index .Summary.Actions "create"}}</h3>
          <p>Create</p>
        </div>
        <div class="summary-card">
          <h3>{{index .Summary.Actions "update"}}</h3>
          <p>Update</p>
        </div>
        <div class="summary-card">
          <h3>{{index .Summary.Actions "delete"}}</h3>
          <p>Delete</p>
        </div>
      </div>
    </div>

    <div class="modules">
      <h2>Resources by Module</h2>
      {{range .Modules}}
      <div class="module">
        <div class="module-header">
          <div class="module-title">{{.Address}}</div>
          <div class="module-info">
            <span>Resources: {{.Summary.ResourceCount}}</span>
            <span>Created: {{index .Summary.Actions "create"}}</span>
            <span>Updated: {{index .Summary.Actions "update"}}</span>
            <span>Deleted: {{index .Summary.Actions "delete"}}</span>
          </div>
        </div>
        <div class="resources">
          {{range .Resources}}
          <div class="resource {{.Action}}" onclick="toggleDetails(this)">
            <div class="resource-info">
              <h4>{{.Address}}</h4>
              <p>{{.Description}}</p>
              <div class="details">
                <pre>{{.DiffText}}</pre>
              </div>
            </div>
            <div class="resource-meta">
              <div class="action-badge action-{{.Action}}">{{.Action}}</div>
              <div class="impact">{{.Impact}}</div>
            </div>
          </div>
          {{end}}
        </div>
      </div>
      {{end}}
    </div>
  </div>

  <script>
    function formatJSON(text) {
      return text.replace(/(\{[^{}]*\}|\[[^\[\]]*\])/g, function(match) {
        try {
          const parsed = JSON.parse(match);
          return JSON.stringify(parsed, null, 2);
        } catch (e) {
          return match;
        }
      });
    }

    function toggleDetails(el) {
      const detail = el.querySelector(".details");
      const pre = detail.querySelector("pre");
      
      if (detail.style.display === "block") {
        detail.style.display = "none";
      } else {
        detail.style.display = "block";
        
        if (!pre.dataset.formatted) {
          const originalText = pre.textContent;
          const formattedText = formatJSON(originalText);
          pre.textContent = formattedText;
          pre.dataset.formatted = "true";
        }
      }
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
	err = tmpl.Execute(&buf, analysis)
	if err != nil {
		fmt.Printf("❌ Error executing template: %v\n", err)
		return "<html><body>Error rendering template</body></html>"
	}

	return buf.String()
}
