# 🛠️ Terraform Plan Visualizer - `tfviz`

Reading a large `terraform plan` output in JSON can be overwhelming, especially in complex infrastructures.  
**`tfviz`** is a simple CLI tool that takes `terraform show -json` output and generates a clean, visual HTML report.  
It summarizes key statistics and shows changes grouped by module and resource, making it easier to understand and share with your team.

<img width="1204" alt="Image" src="https://github.com/user-attachments/assets/5ac07a64-aaa1-480b-aaf5-89823290ab81" />

## ⚙️ Requirements

- [Go](https://go.dev/dl/) 1.18 or higher
- [Terraform](https://www.terraform.io/downloads) 0.12 or higher
- Terraform plan output in JSON format (`terraform show -json`)

## ⚙️ Usage

### 1. Install Go

Make sure [Go](https://go.dev/dl/) is installed on your system.

### 2. Build the binary

```bash
go build -o tfviz main.go
```

### 3. (Optional) Move to global path
```bash
sudo mv tfviz /usr/local/bin/
```

### 4. (Optional) Change the default port (9876)
By default, tfviz runs on port 9876.
To change it, modify the port variable in the serveHTMLOnce function inside main.go:

```go
func serveHTMLOnce(html string) {
    port := "9876" // ← Change this if needed
    ...
}
```

### 5. Visualize your Terraform plan
In your Terraform project directory:

```bash
tfviz plan
```

This will start a temporary local web server and automatically open your default browser to show the visualized plan.
No HTML file is written to disk — everything runs in memory.
Once you close the browser or exit the page, the server automatically shuts down.