package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func loadTestPlan(t *testing.T) TerraformPlan {
	t.Helper()
	data, err := os.ReadFile("plan.json")
	if err != nil {
		t.Fatalf("failed to read plan.json: %v", err)
	}
	var plan TerraformPlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatalf("failed to parse plan.json: %v", err)
	}
	return plan
}

func TestContainmentMap(t *testing.T) {
	plan := loadTestPlan(t)
	containment := buildContainmentMap(plan.Configuration)

	tests := []struct {
		child  string
		parent string
	}{
		// VPC infrastructure hierarchy
		{"module.vpc.aws_subnet.public_subnet", "module.vpc.aws_vpc.vpc"},
		{"module.vpc.aws_internet_gateway.igw", "module.vpc.aws_vpc.vpc"},
		{"module.vpc.aws_route_table.public_rtb", "module.vpc.aws_vpc.vpc"},
		{"module.vpc.aws_egress_only_internet_gateway.egress", "module.vpc.aws_vpc.vpc"},
		// Routes inside route tables (typed rule, priority 30 beats vpc_id 100)
		{"module.vpc.aws_route.public_internet_gateway", "module.vpc.aws_route_table.public_rtb"},
		// RTAs inside route tables (typed rule, priority 30 beats subnet_id 50)
		{"module.vpc.aws_route_table_association.public_rtb", "module.vpc.aws_route_table.public_rtb"},
		// Beanstalk: env and version inside application
		{"module.beanstalk.module.calc_efs.aws_elastic_beanstalk_environment.app", "module.beanstalk.module.calc_efs.aws_elastic_beanstalk_application.app"},
		{"module.beanstalk.module.calc_efs.aws_elastic_beanstalk_application_version.app", "module.beanstalk.module.calc_efs.aws_elastic_beanstalk_application.app"},
		// IAM: policy attachments and instance profile inside their role
		{"module.beanstalk.module.calc_efs.aws_iam_role_policy_attachment.app", "module.beanstalk.module.calc_efs.aws_iam_role.app_service_role"},
		{"module.beanstalk.module.calc_efs.aws_iam_role_policy_attachment.app_instance_profile_docker", "module.beanstalk.module.calc_efs.aws_iam_role.app_instance_profile_role"},
		{"module.beanstalk.module.calc_efs.aws_iam_instance_profile.app_ec2_role", "module.beanstalk.module.calc_efs.aws_iam_role.app_instance_profile_role"},
	}

	for _, tt := range tests {
		parent, ok := containment[tt.child]
		if !ok || parent != tt.parent {
			t.Errorf("containment[%q]:\n  want %q\n  got  %q (ok=%v)", tt.child, tt.parent, parent, ok)
		}
	}

	// VPC itself should NOT have a parent
	if _, ok := containment["module.vpc.aws_vpc.vpc"]; ok {
		t.Error("VPC itself should not have a containment parent")
	}
}

func TestBuildGraphJSON_Containment(t *testing.T) {
	plan := loadTestPlan(t)
	analyzed := analyzePlan(plan)
	refEdges := buildRefEdges(plan.Configuration)
	containment := buildContainmentMap(plan.Configuration)
	allPlanned := collectAllPlannedResources(plan.PlannedValues.RootModule)
	enrichContainmentFromValues(allPlanned, containment)
	plannedValues := buildPlannedValuesMap(allPlanned)

	graphJSON, _, err := buildGraphJSON(analyzed, refEdges, containment, plannedValues)
	if err != nil {
		t.Fatalf("buildGraphJSON error: %v", err)
	}

	type elemData struct {
		Data    map[string]interface{} `json:"data"`
		Classes string                 `json:"classes"`
	}
	var elements []elemData
	if err := json.Unmarshal([]byte(graphJSON), &elements); err != nil {
		t.Fatalf("failed to parse graph JSON: %v", err)
	}

	nodeParents := map[string]string{}
	for _, el := range elements {
		id, _ := el.Data["id"].(string)
		if id == "" || strings.HasPrefix(id, "edge:") {
			continue
		}
		if parent, ok := el.Data["parent"]; ok {
			nodeParents[id] = parent.(string)
		}
	}

	// All VPC module resources with indexed addresses should resolve to VPC
	vpcChildren := []string{
		"module.vpc.aws_subnet.public_subnet",
		"module.vpc.aws_route_table.public_rtb",
		"module.vpc.aws_route.public_internet_gateway",
		"module.vpc.aws_route_table_association.public_rtb",
	}
	for _, base := range vpcChildren {
		for _, idx := range []string{"[0]", "[1]", "[2]"} {
			id := base + idx
			parent := nodeParents[id]
			if parent != "module.vpc.aws_vpc.vpc" {
				t.Errorf("%s: expected parent=module.vpc.aws_vpc.vpc, got %q", id, parent)
			}
		}
	}

	// Non-indexed VPC children
	for _, id := range []string{
		"module.vpc.aws_internet_gateway.igw",
		"module.vpc.aws_egress_only_internet_gateway.egress",
	} {
		if parent := nodeParents[id]; parent != "module.vpc.aws_vpc.vpc" {
			t.Errorf("%s: expected parent=module.vpc.aws_vpc.vpc, got %q", id, parent)
		}
	}

	// Beanstalk: env inside application
	ebApp := "module.beanstalk.module.calc_efs.aws_elastic_beanstalk_application.app"
	if parent := nodeParents["module.beanstalk.module.calc_efs.aws_elastic_beanstalk_environment.app"]; parent != ebApp {
		t.Errorf("EB env: expected parent=%s, got %q", ebApp, parent)
	}

	// IAM: policy attachments inside their role
	svcRole := "module.beanstalk.module.calc_efs.aws_iam_role.app_service_role"
	if parent := nodeParents["module.beanstalk.module.calc_efs.aws_iam_role_policy_attachment.app"]; parent != svcRole {
		t.Errorf("policy attachment: expected parent=%s, got %q", svcRole, parent)
	}

	instRole := "module.beanstalk.module.calc_efs.aws_iam_role.app_instance_profile_role"
	for _, suffix := range []string{"_docker", "_ssm", "_autoscaling", "_efs", "_manage"} {
		id := "module.beanstalk.module.calc_efs.aws_iam_role_policy_attachment.app_instance_profile" + suffix
		if parent := nodeParents[id]; parent != instRole {
			t.Errorf("%s: expected parent=%s, got %q", id, instRole, parent)
		}
	}

	// No synthetic nodes with buggy labels
	for _, el := range elements {
		label, _ := el.Data["label"].(string)
		if strings.Contains(label, "(module)") {
			id, _ := el.Data["id"].(string)
			t.Errorf("found node with buggy module label: id=%q label=%q", id, label)
		}
	}

	// VPC has no parent
	if _, ok := nodeParents["module.vpc.aws_vpc.vpc"]; ok {
		t.Error("VPC should have no parent")
	}
}

func TestStripIndex(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"module.vpc.aws_subnet.public_subnet[0]", "module.vpc.aws_subnet.public_subnet"},
		{"module.vpc.aws_subnet.public_subnet[\"key\"]", "module.vpc.aws_subnet.public_subnet"},
		{"module.vpc.aws_vpc.vpc", "module.vpc.aws_vpc.vpc"},
		{"aws_instance.web[0]", "aws_instance.web"},
	}
	for _, tt := range tests {
		got := stripIndex(tt.input)
		if got != tt.expected {
			t.Errorf("stripIndex(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestMatchesChildType(t *testing.T) {
	// Empty patterns = wildcard
	if !matchesChildType("aws_anything", nil) {
		t.Error("nil patterns should match any type")
	}
	// Prefix matching
	if !matchesChildType("aws_iam_role_policy_attachment", []string{"aws_iam_role_policy"}) {
		t.Error("should match prefix aws_iam_role_policy")
	}
	if matchesChildType("aws_iam_policy", []string{"aws_iam_role_policy"}) {
		t.Error("should not match aws_iam_policy against aws_iam_role_policy prefix")
	}
	// Exact match
	if !matchesChildType("aws_route", []string{"aws_route", "aws_route_table_association"}) {
		t.Error("should match exact aws_route")
	}
}

func TestEnrichContainmentFromValues(t *testing.T) {
	// Simulate planned resources: a VPC, a subnet inside it, and a Lambda with vpc_config
	planned := []Resource{
		{
			Address: "aws_vpc.main",
			Type:    "aws_vpc",
			Values: map[string]interface{}{
				"id":         "vpc-abc123",
				"cidr_block": "10.0.0.0/16",
			},
		},
		{
			Address: "aws_subnet.private[0]",
			Type:    "aws_subnet",
			Values: map[string]interface{}{
				"id":                "subnet-def456",
				"vpc_id":           "vpc-abc123",
				"cidr_block":       "10.0.1.0/24",
				"availability_zone": "ap-northeast-2a",
			},
		},
		{
			Address: "module.lambda.aws_lambda_function.api",
			Type:    "aws_lambda_function",
			Values: map[string]interface{}{
				"function_name": "api-handler",
				"vpc_config": []interface{}{
					map[string]interface{}{
						"subnet_ids":         []interface{}{"subnet-def456"},
						"security_group_ids": []interface{}{"sg-xyz789"},
						"vpc_id":             "vpc-abc123",
					},
				},
			},
		},
	}

	// Start with empty containment (simulating config-based resolution failing)
	containment := map[string]string{}
	enrichContainmentFromValues(planned, containment)

	// Lambda should be contained in the subnet (subnet_ids takes priority over vpc_id)
	if parent, ok := containment["module.lambda.aws_lambda_function.api"]; !ok || parent != "aws_subnet.private" {
		t.Errorf("Lambda: want parent=aws_subnet.private, got %q (ok=%v)", parent, ok)
	}

	// Subnet should be contained in the VPC via vpc_id
	if parent, ok := containment["aws_subnet.private"]; !ok || parent != "aws_vpc.main" {
		t.Errorf("Subnet: want parent=aws_vpc.main, got %q (ok=%v)", parent, ok)
	}

	// VPC should have no parent
	if _, ok := containment["aws_vpc.main"]; ok {
		t.Error("VPC should have no containment parent")
	}
}

func TestEnrichContainmentFromValues_NoOverride(t *testing.T) {
	// If config-based containment already resolved, value-based should NOT override
	planned := []Resource{
		{
			Address: "aws_vpc.main",
			Type:    "aws_vpc",
			Values:  map[string]interface{}{"id": "vpc-abc123"},
		},
		{
			Address: "aws_instance.web",
			Type:    "aws_instance",
			Values: map[string]interface{}{
				"subnet_id": "subnet-xxx",
				"vpc_id":    "vpc-abc123",
			},
		},
	}

	containment := map[string]string{
		"aws_instance.web": "aws_subnet.already_resolved",
	}
	enrichContainmentFromValues(planned, containment)

	// Should keep the existing parent, not override with vpc_id
	if parent := containment["aws_instance.web"]; parent != "aws_subnet.already_resolved" {
		t.Errorf("should not override existing containment, got %q", parent)
	}
}

func TestEnrichNetworkLabel(t *testing.T) {
	// VPC with CIDR and Name tag
	vpcValues := map[string]interface{}{
		"cidr_block": "10.0.0.0/16",
		"tags":       map[string]interface{}{"Name": "prod-vpc"},
	}
	label := enrichNetworkLabel("main\n(aws_vpc)", "aws_vpc", vpcValues)
	if !strings.Contains(label, "prod-vpc") || !strings.Contains(label, "10.0.0.0/16") {
		t.Errorf("VPC label missing info: %q", label)
	}

	// Subnet with CIDR, AZ, and Name tag
	subnetValues := map[string]interface{}{
		"cidr_block":        "10.0.1.0/24",
		"availability_zone": "ap-northeast-2a",
		"tags":              map[string]interface{}{"Name": "private-a"},
	}
	label = enrichNetworkLabel("private\n(aws_subnet)", "aws_subnet", subnetValues)
	if !strings.Contains(label, "private-a") || !strings.Contains(label, "10.0.1.0/24") || !strings.Contains(label, "ap-northeast-2a") {
		t.Errorf("Subnet label missing info: %q", label)
	}

	// Nil values should not crash
	label = enrichNetworkLabel("vpc\n(aws_vpc)", "aws_vpc", nil)
	if label != "vpc\n(aws_vpc)" {
		t.Errorf("nil values should return original label, got %q", label)
	}
}

func TestModuleOutputTracing(t *testing.T) {
	// Simulates: module.lambda { subnet_ids = module.vpc.public_subnets_id }
	// where module.vpc has output public_subnets_id → aws_subnet.public_subnet
	config := PlanConfiguration{
		RootModule: ConfigModule{
			ModuleCalls: map[string]ConfigModuleCall{
				"vpc": {
					Module: ConfigModule{
						Resources: []ConfigResource{
							{Address: "aws_vpc.main", Type: "aws_vpc", Expressions: map[string]interface{}{}},
							{
								Address: "aws_subnet.public", Type: "aws_subnet",
								Expressions: map[string]interface{}{
									"vpc_id": map[string]interface{}{"references": []interface{}{"aws_vpc.main.id", "aws_vpc.main"}},
								},
							},
						},
						Outputs: map[string]ConfigOutput{
							"vpc_id":            {Expression: map[string]interface{}{"references": []interface{}{"aws_vpc.main.id", "aws_vpc.main"}}},
							"public_subnets_id": {Expression: map[string]interface{}{"references": []interface{}{"aws_subnet.public"}}},
						},
					},
				},
				"lambda": {
					Expressions: map[string]interface{}{
						"subnet_ids": map[string]interface{}{"references": []interface{}{"module.vpc.public_subnets_id", "module.vpc"}},
						"vpc_id":     map[string]interface{}{"references": []interface{}{"module.vpc.vpc_id", "module.vpc"}},
					},
					Module: ConfigModule{
						Resources: []ConfigResource{
							{
								Address: "aws_lambda_function.api", Type: "aws_lambda_function",
								Expressions: map[string]interface{}{
									"function_name": map[string]interface{}{"constant_value": "api"},
									"vpc_config": []interface{}{
										map[string]interface{}{
											"subnet_ids":         map[string]interface{}{"references": []interface{}{"var.subnet_ids"}},
											"security_group_ids": map[string]interface{}{"references": []interface{}{"var.sg_ids"}},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Test output map
	outputMap := buildModuleOutputMap(config)
	if outputMap["module.vpc.vpc_id"] != "module.vpc.aws_vpc.main" {
		t.Errorf("output map vpc_id: got %q", outputMap["module.vpc.vpc_id"])
	}
	if outputMap["module.vpc.public_subnets_id"] != "module.vpc.aws_subnet.public" {
		t.Errorf("output map public_subnets_id: got %q", outputMap["module.vpc.public_subnets_id"])
	}

	// Test containment: Lambda → subnet (via module output chain)
	containment := buildContainmentMap(config)

	// Lambda should be contained in module.vpc.aws_subnet.public (priority 50 for subnet_ids)
	if parent, ok := containment["module.lambda.aws_lambda_function.api"]; !ok || parent != "module.vpc.aws_subnet.public" {
		t.Errorf("Lambda containment: want parent=module.vpc.aws_subnet.public, got %q (ok=%v)", parent, ok)
	}

	// Subnet should be contained in VPC
	if parent, ok := containment["module.vpc.aws_subnet.public"]; !ok || parent != "module.vpc.aws_vpc.main" {
		t.Errorf("Subnet containment: want parent=module.vpc.aws_vpc.main, got %q (ok=%v)", parent, ok)
	}
}

func TestModuleOutputTracing_NestedModules(t *testing.T) {
	// Simulates: root → module.beanstalk → module.beanstalk.module.calc_efs
	// where root passes vpc_id = module.vpc.vpc_id through nested module calls
	config := PlanConfiguration{
		RootModule: ConfigModule{
			ModuleCalls: map[string]ConfigModuleCall{
				"vpc": {
					Module: ConfigModule{
						Resources: []ConfigResource{
							{Address: "aws_vpc.vpc", Type: "aws_vpc", Expressions: map[string]interface{}{}},
						},
						Outputs: map[string]ConfigOutput{
							"vpc_id": {Expression: map[string]interface{}{"references": []interface{}{"aws_vpc.vpc.id", "aws_vpc.vpc"}}},
						},
					},
				},
				"app": {
					Expressions: map[string]interface{}{
						"vpc_id": map[string]interface{}{"references": []interface{}{"module.vpc.vpc_id", "module.vpc"}},
					},
					Module: ConfigModule{
						ModuleCalls: map[string]ConfigModuleCall{
							"inner": {
								Expressions: map[string]interface{}{
									"vpc_id": map[string]interface{}{"references": []interface{}{"var.vpc_id"}},
								},
								Module: ConfigModule{
									Resources: []ConfigResource{
										{
											Address: "aws_security_group.sg", Type: "aws_security_group",
											Expressions: map[string]interface{}{
												"vpc_id": map[string]interface{}{"references": []interface{}{"var.vpc_id"}},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}

	containment := buildContainmentMap(config)

	// Security group in module.app.module.inner should trace through var chain to module.vpc.aws_vpc.vpc
	if parent, ok := containment["module.app.module.inner.aws_security_group.sg"]; !ok || parent != "module.vpc.aws_vpc.vpc" {
		t.Errorf("SG containment: want parent=module.vpc.aws_vpc.vpc, got %q (ok=%v)", parent, ok)
	}
}

func TestIsModuleOnlyPrefix(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"module.vpc", true},
		{"module.a.module.b", true},
		{"module.vpc.aws_vpc.main", false},
		{"aws_vpc.main", false},
		{"module.vpc.aws_subnet.public", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isModuleOnlyPrefix(tt.input)
		if got != tt.expected {
			t.Errorf("isModuleOnlyPrefix(%q) = %v, want %v", tt.input, got, tt.expected)
		}
	}
}
