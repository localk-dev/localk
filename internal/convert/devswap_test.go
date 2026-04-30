package convert

import (
	"strings"
	"testing"

	"github.com/localk-dev/localk/internal/compose"
)

func TestApplyDevSwap_BitnamiMongoSwapsToMongo7(t *testing.T) {
	main := &compose.Service{
		Image: "docker.io/bitnami/mongodb:7.0",
		Environment: map[string]string{
			"MONGODB_REPLICA_SET_MODE":     "primary",
			"MONGODB_REPLICA_SET_NAME":     "rs0",
			"MONGODB_INITIAL_PRIMARY_HOST": "mongodb-0.mongodb-headless.default.svc.cluster.local",
			"MONGODB_ROOT_USER":            "root",
			"MONGODB_ROOT_PASSWORD":        "supersecret",
			"MY_POD_NAME":                  "mongodb-0",
			"BITNAMI_DEBUG":                "false",
			"K8S_SERVICE_NAME":             "mongodb-headless",
			"OPENSSL_FIPS":                 "no",
		},
		Volumes: []any{
			"./configs/mongodb-scripts/setup.sh:/scripts/setup.sh:ro",
			"./configs/mongodb-common-scripts:/bitnami/scripts",
			"./secrets/mongodb:/opt/bitnami/mongodb/secrets",
			"mongodb-datadir:/data/db",
		},
		Command:    []string{"/scripts/setup.sh"},
		Entrypoint: []string{"/bin/bash"},
		DependsOn: map[string]compose.DependsOnSpec{
			"mongodb-headless-log-dir": {Condition: "service_completed_successfully"},
		},
	}
	extras := map[string]compose.Service{
		"mongodb-headless-log-dir": {
			Image:   "docker.io/bitnami/mongodb:7.0",
			Restart: "no",
		},
	}

	msg := applyDevSwap("mongodb-headless", main, extras, false)
	if msg == "" {
		t.Fatalf("expected dev-swap warning, got empty")
	}
	if !strings.Contains(msg, "mongo:7") {
		t.Errorf("warning should name the dev image, got %q", msg)
	}

	if main.Image != "mongo:7" {
		t.Errorf("image should be mongo:7, got %q", main.Image)
	}
	// Auth env translated to vanilla names.
	if main.Environment["MONGO_INITDB_ROOT_USERNAME"] != "root" {
		t.Errorf("MONGO_INITDB_ROOT_USERNAME = %q, want %q", main.Environment["MONGO_INITDB_ROOT_USERNAME"], "root")
	}
	if main.Environment["MONGO_INITDB_ROOT_PASSWORD"] != "supersecret" {
		t.Errorf("MONGO_INITDB_ROOT_PASSWORD = %q, want %q", main.Environment["MONGO_INITDB_ROOT_PASSWORD"], "supersecret")
	}
	// Chart-specific env stripped.
	for _, k := range []string{
		"MONGODB_REPLICA_SET_MODE",
		"MONGODB_INITIAL_PRIMARY_HOST",
		"MONGODB_ROOT_USER",
		"MY_POD_NAME",
		"BITNAMI_DEBUG",
		"K8S_SERVICE_NAME",
	} {
		if _, present := main.Environment[k]; present {
			t.Errorf("chart env %q should be stripped, still present", k)
		}
	}
	// Chart-specific bind mounts dropped, datadir kept.
	for _, v := range main.Volumes {
		s := v.(string)
		if strings.Contains(s, "/bitnami/") || strings.Contains(s, "/opt/bitnami/") {
			t.Errorf("chart-specific volume should be dropped, still present: %q", s)
		}
	}
	hasDatadir := false
	for _, v := range main.Volumes {
		if s := v.(string); s == "mongodb-datadir:/data/db" {
			hasDatadir = true
		}
	}
	if !hasDatadir {
		t.Errorf("PVC datadir should survive the swap, got %v", main.Volumes)
	}
	// Init container dropped + depends_on cleared.
	if _, present := extras["mongodb-headless-log-dir"]; present {
		t.Errorf("chart init container should be dropped from extras")
	}
	if main.DependsOn != nil {
		t.Errorf("DependsOn should be nil after init drop, got %v", main.DependsOn)
	}
	// Command/entrypoint cleared so vanilla image's defaults run.
	if main.Command != nil || main.Entrypoint != nil {
		t.Errorf("Command/Entrypoint should be cleared, got Command=%v Entrypoint=%v", main.Command, main.Entrypoint)
	}
}

func TestApplyDevSwap_BitnamiRabbitSwapsToVanilla(t *testing.T) {
	main := &compose.Service{
		Image: "docker.io/bitnamilegacy/rabbitmq:4.1.3-debian-12-r1",
		Environment: map[string]string{
			"RABBITMQ_NODE_NAME":     "rabbit@rabbitmq.rabbitmq-headless.default.svc.cluster.local",
			"RABBITMQ_USE_LONGNAME":  "true",
			"RABBITMQ_USERNAME":      "user",
			"RABBITMQ_PASSWORD":      "secretpass",
			"RABBITMQ_PLUGINS":       "rabbitmq_management, rabbitmq_peer_discovery_k8s",
			"RABBITMQ_LDAP_ENABLE":   "no",
			"BITNAMI_DEBUG":          "false",
			"MY_POD_IP":              "rabbitmq",
			"K8S_SERVICE_NAME":       "rabbitmq-headless",
		},
	}
	extras := map[string]compose.Service{}

	msg := applyDevSwap("rabbitmq", main, extras, false)
	if msg == "" {
		t.Fatalf("expected dev-swap warning")
	}
	if main.Image != "rabbitmq:3-management" {
		t.Errorf("image should be rabbitmq:3-management, got %q", main.Image)
	}
	if main.Environment["RABBITMQ_DEFAULT_USER"] != "user" {
		t.Errorf("RABBITMQ_DEFAULT_USER = %q, want %q", main.Environment["RABBITMQ_DEFAULT_USER"], "user")
	}
	if main.Environment["RABBITMQ_DEFAULT_PASS"] != "secretpass" {
		t.Errorf("RABBITMQ_DEFAULT_PASS = %q, want %q", main.Environment["RABBITMQ_DEFAULT_PASS"], "secretpass")
	}
	for _, k := range []string{"RABBITMQ_NODE_NAME", "RABBITMQ_USE_LONGNAME", "RABBITMQ_PLUGINS", "MY_POD_IP", "BITNAMI_DEBUG"} {
		if _, present := main.Environment[k]; present {
			t.Errorf("chart env %q should be stripped, still present", k)
		}
	}
}

// TestApplyDevSwap_PreserveImageOptOut verifies the localk.yaml
// `preserve_image: true` knob actually keeps the chart image intact.
// Required for users with custom plugin builds, TLS-enabled Bitnami
// images, etc.
func TestApplyDevSwap_PreserveImageOptOut(t *testing.T) {
	main := &compose.Service{
		Image: "bitnami/mongodb:7.0",
		Environment: map[string]string{
			"MONGODB_REPLICA_SET_MODE": "primary",
		},
	}
	msg := applyDevSwap("mongodb", main, nil, true)
	if msg != "" {
		t.Errorf("preserve_image=true should suppress dev-swap, got warning %q", msg)
	}
	if main.Image != "bitnami/mongodb:7.0" {
		t.Errorf("image should be unchanged with preserve_image=true, got %q", main.Image)
	}
}

// TestApplyDevSwap_SkipsNonClusteredBitnami: a Bitnami image used as
// a plain standalone (no cluster env signals) should NOT be swapped.
// Many users run Bitnami images for the patched/scanned image alone.
func TestApplyDevSwap_SkipsNonClusteredBitnami(t *testing.T) {
	main := &compose.Service{
		Image: "bitnami/mongodb:7.0",
		Environment: map[string]string{
			"MONGODB_ROOT_USER":     "root",
			"MONGODB_ROOT_PASSWORD": "secret",
			// no REPLICA_SET_* / INITIAL_PRIMARY_HOST → not clustered
		},
	}
	msg := applyDevSwap("mongo", main, nil, false)
	if msg != "" {
		t.Errorf("non-clustered Bitnami image should NOT trigger swap, got %q", msg)
	}
	if main.Image != "bitnami/mongodb:7.0" {
		t.Errorf("image should be unchanged for non-clustered case, got %q", main.Image)
	}
}

// TestApplyDevSwap_LeavesUnknownImages confirms the rule list is
// closed — random non-chart images aren't accidentally swapped.
func TestApplyDevSwap_LeavesUnknownImages(t *testing.T) {
	main := &compose.Service{
		Image: "ghcr.io/example/app:1.0",
		Environment: map[string]string{
			"DATABASE_URL": "postgres://...",
		},
	}
	msg := applyDevSwap("app", main, nil, false)
	if msg != "" {
		t.Errorf("non-chart image should be ignored, got swap warning %q", msg)
	}
	if main.Image != "ghcr.io/example/app:1.0" {
		t.Errorf("image should be unchanged, got %q", main.Image)
	}
}

func TestImageMatches(t *testing.T) {
	cases := []struct {
		image    string
		prefixes []string
		want     bool
	}{
		{"bitnami/mongodb:7.0", []string{"bitnami/mongodb"}, true},
		{"docker.io/bitnami/mongodb:latest", []string{"bitnami/mongodb"}, true},
		{"registry-1.docker.io/bitnami/mongodb:5.0-debian-11", []string{"bitnami/mongodb"}, true},
		{"docker.io/bitnamilegacy/rabbitmq:4.1.3", []string{"bitnami/rabbitmq", "bitnamilegacy/rabbitmq"}, true},
		{"mongo:7", []string{"bitnami/mongodb"}, false},
		// Sibling repos (mongodb-exporter, mongo, etc.) must NOT match
		// — they're different images that happen to share a prefix.
		// Otherwise we'd incorrectly swap the exporter to mongo:7.
		{"bitnami/mongodb-exporter:0.39", []string{"bitnami/mongodb"}, false},
		{"bitnami/mongo:7", []string{"bitnami/mongodb"}, false},
		{"", []string{"bitnami/mongodb"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.image, func(t *testing.T) {
			if got := imageMatches(tc.image, tc.prefixes...); got != tc.want {
				t.Errorf("imageMatches(%q, %v) = %v, want %v", tc.image, tc.prefixes, got, tc.want)
			}
		})
	}
}

func TestMatchesChartRoot(t *testing.T) {
	cases := []struct {
		mount string
		roots []string
		want  bool
	}{
		// Short form "target" only
		{"/opt/bitnami/mongodb/data", []string{"/opt/bitnami/mongodb"}, true},
		// "source:target"
		{"./configs/mongodb-scripts:/bitnami/scripts", []string{"/bitnami/mongodb", "/opt/bitnami/mongodb"}, false},
		{"./configs/mongodb-scripts:/bitnami/scripts", []string{"/bitnami"}, true},
		// "source:target:mode"
		{"./secrets/mongodb:/opt/bitnami/mongodb/secrets:ro", []string{"/opt/bitnami/mongodb"}, true},
		// PVC mount that should NOT match the chart root
		{"mongodb-datadir:/data/db", []string{"/opt/bitnami/mongodb"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.mount, func(t *testing.T) {
			if got := matchesChartRoot(tc.mount, tc.roots...); got != tc.want {
				t.Errorf("matchesChartRoot(%q, %v) = %v, want %v", tc.mount, tc.roots, got, tc.want)
			}
		})
	}
}

func TestDropInitContainers(t *testing.T) {
	main := &compose.Service{
		DependsOn: map[string]compose.DependsOnSpec{
			"mongodb-headless-log-dir": {Condition: "service_completed_successfully"},
			"mongodb-headless-sidecar": {Condition: "service_started"},
		},
	}
	extras := map[string]compose.Service{
		// Init container (Restart="no") owned by this workload — drop.
		"mongodb-headless-log-dir": {Restart: "no"},
		// Sidecar (Restart unset / "unless-stopped") — keep.
		"mongodb-headless-sidecar": {Restart: "unless-stopped"},
		// Different workload's init — leave alone.
		"other-workload-init": {Restart: "no"},
	}
	dropInitContainers("mongodb-headless", main, extras)

	if _, ok := extras["mongodb-headless-log-dir"]; ok {
		t.Errorf("init container should be dropped")
	}
	if _, ok := extras["mongodb-headless-sidecar"]; !ok {
		t.Errorf("sidecar should be kept")
	}
	if _, ok := extras["other-workload-init"]; !ok {
		t.Errorf("other workload's init should be untouched")
	}
	if _, ok := main.DependsOn["mongodb-headless-log-dir"]; ok {
		t.Errorf("DependsOn entry for dropped init should be cleared")
	}
	if _, ok := main.DependsOn["mongodb-headless-sidecar"]; !ok {
		t.Errorf("DependsOn entry for sidecar should remain")
	}
}
