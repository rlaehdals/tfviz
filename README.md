# 🛠️ Terraform Plan Visualizer - `tfviz`

Reading a large `terraform plan` output in JSON can be overwhelming, especially in complex infrastructures.  
**`tfviz`** is a simple CLI tool that takes `terraform show -json` output and generates a clean, visual HTML report.  
It summarizes key statistics and shows changes grouped by module and resource, making it easier to understand and share with your team.

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

### 4. Visualize your Terraform plan
In your Terraform project directory:

```bash
tfviz plan
```

This will start a temporary local web server and automatically open your default browser to show the visualized plan.
No HTML file is written to disk — everything runs in memory.
Once you close the browser or exit the page, the server automatically shuts down.