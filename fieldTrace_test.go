package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/kustomize/api/filesys"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/yaml"
)

func TestProcessKustomization(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "fieldtrace-test-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create test kustomization structure
	testDir := filepath.Join(tmpDir, "test")
	err = os.MkdirAll(testDir, 0755)
	assert.NoError(t, err)

	// Create a test kustomization.yaml
	kustContent := `
resources:
  - base
patches:
  - path: patches/patch1.yaml
    target:
      kind: Deployment
      name: test
  - patch: |-
      apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: test
    target:
      kind: Deployment
      name: test
`
	err = os.WriteFile(filepath.Join(testDir, "kustomization.yaml"), []byte(kustContent), 0644)
	assert.NoError(t, err)

	// Create base directory and resource
	baseDir := filepath.Join(testDir, "base")
	err = os.MkdirAll(baseDir, 0755)
	assert.NoError(t, err)

	// Create base kustomization.yaml
	baseKustContent := `
resources:
  - deployment.yaml
`
	err = os.WriteFile(filepath.Join(baseDir, "kustomization.yaml"), []byte(baseKustContent), 0644)
	assert.NoError(t, err)

	baseContent := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test
spec:
  replicas: 1
`
	err = os.WriteFile(filepath.Join(baseDir, "deployment.yaml"), []byte(baseContent), 0644)
	assert.NoError(t, err)

	// Create patches directory and patch file
	patchesDir := filepath.Join(testDir, "patches")
	err = os.MkdirAll(patchesDir, 0755)
	assert.NoError(t, err)

	patchContent := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test
spec:
  replicas: 3
`
	err = os.WriteFile(filepath.Join(patchesDir, "patch1.yaml"), []byte(patchContent), 0644)
	assert.NoError(t, err)

	// Test processKustomization
	fs := filesys.MakeFsOnDisk()
	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	allPatches := make([]types.Patch, 0)
	allResources := make(map[string]*resource.Resource)

	processKustomization(fs, k, testDir, &allPatches, allResources)

	// Verify patches were collected
	assert.Equal(t, 2, len(allPatches), "Should collect both patches")
	assert.Equal(t, filepath.Join("patches", "patch1.yaml"), allPatches[0].Path, "First patch should be file-based")
	assert.Equal(t, "", allPatches[1].Path, "Second patch should be inline")

	// Verify resources were collected
	assert.Equal(t, 1, len(allResources), "Should collect one resource")
	_, exists := allResources["Deployment/test"]
	assert.True(t, exists, "Should find Deployment/test resource")
}

func TestFieldSourceTracking(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "fieldtrace-test-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create test kustomization structure
	testDir := filepath.Join(tmpDir, "test")
	err = os.MkdirAll(testDir, 0755)
	assert.NoError(t, err)

	// Create a test kustomization.yaml with strategic merge patch
	kustContent := `
resources:
  - base
patches:
  - path: patches/patch1.yaml
    target:
      kind: Deployment
      name: test
`
	err = os.WriteFile(filepath.Join(testDir, "kustomization.yaml"), []byte(kustContent), 0644)
	assert.NoError(t, err)

	// Create base resource
	baseDir := filepath.Join(testDir, "base")
	err = os.MkdirAll(baseDir, 0755)
	assert.NoError(t, err)

	// Create base kustomization.yaml
	baseKustContent := `
resources:
  - deployment.yaml
`
	err = os.WriteFile(filepath.Join(baseDir, "kustomization.yaml"), []byte(baseKustContent), 0644)
	assert.NoError(t, err)

	baseContent := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: test
        image: test:1.0
`
	err = os.WriteFile(filepath.Join(baseDir, "deployment.yaml"), []byte(baseContent), 0644)
	assert.NoError(t, err)

	// Create patch file
	patchesDir := filepath.Join(testDir, "patches")
	err = os.MkdirAll(patchesDir, 0755)
	assert.NoError(t, err)

	patchContent := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test
spec:
  replicas: 3
  template:
    spec:
      containers:
      - name: test
        image: test:2.0
`
	err = os.WriteFile(filepath.Join(patchesDir, "patch1.yaml"), []byte(patchContent), 0644)
	assert.NoError(t, err)

	// Run the main processing
	fs := filesys.MakeFsOnDisk()
	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	allPatches := make([]types.Patch, 0)
	allResources := make(map[string]*resource.Resource)

	processKustomization(fs, k, testDir, &allPatches, allResources)

	// Process patches and track changes
	for _, patch := range allPatches {
		targetKey := fmt.Sprintf("%s/%s", patch.Target.Kind, patch.Target.Name)
		targetRes, exists := allResources[targetKey]
		assert.True(t, exists, "Target resource should exist")

		// Get state before patch
		var beforeMap map[string]interface{}
		err := yaml.Unmarshal([]byte(targetRes.MustYaml()), &beforeMap)
		assert.NoError(t, err)

		// Apply patch
		var patchData []byte
		if patch.Path != "" {
			patchData, err = fs.ReadFile(patch.Path)
			assert.NoError(t, err)
		} else {
			patchData = []byte(patch.Patch)
		}

		// Parse and apply patch
		var patchContent interface{}
		err = yaml.Unmarshal(patchData, &patchContent)
		assert.NoError(t, err)

		// Convert resource to map
		var resourceMap map[string]interface{}
		err = yaml.Unmarshal([]byte(targetRes.MustYaml()), &resourceMap)
		assert.NoError(t, err)

		// Apply strategic merge patch
		mergeMap(resourceMap, patchContent.(map[string]interface{}))
	}

	// Verify field changes were tracked
	assert.Greater(t, len(fieldSources), 0, "Should track field changes")

	// Check for specific changes
	foundReplicasChange := false
	foundImageChange := false
	for _, source := range fieldSources {
		if strings.Join(source.Path, " → ") == "spec → replicas" {
			foundReplicasChange = true
			assert.Equal(t, float64(1), source.Original, "Original replicas should be 1")
			assert.Equal(t, float64(3), source.New, "New replicas should be 3")
		}
		if strings.Join(source.Path, " → ") == "spec → template → spec → containers → 0 → image" {
			foundImageChange = true
			assert.Equal(t, "test:1.0", source.Original, "Original image should be test:1.0")
			assert.Equal(t, "test:2.0", source.New, "New image should be test:2.0")
		}
	}
	assert.True(t, foundReplicasChange, "Should track replicas change")
	assert.True(t, foundImageChange, "Should track image change")
}

func TestPathResolution(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir, err := os.MkdirTemp("", "fieldtrace-test-*")
	assert.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	// Create test kustomization structure with nested kustomizations
	rootDir := filepath.Join(tmpDir, "root")
	err = os.MkdirAll(rootDir, 0755)
	assert.NoError(t, err)

	// Create root kustomization.yaml
	rootKustContent := `
resources:
  - base
components:
  - component
`
	err = os.WriteFile(filepath.Join(rootDir, "kustomization.yaml"), []byte(rootKustContent), 0644)
	assert.NoError(t, err)

	// Create base kustomization
	baseDir := filepath.Join(rootDir, "base")
	err = os.MkdirAll(baseDir, 0755)
	assert.NoError(t, err)

	baseKustContent := `
resources:
  - resource.yaml
patches:
  - path: patches/patch1.yaml
    target:
      kind: Deployment
      name: test
`
	err = os.WriteFile(filepath.Join(baseDir, "kustomization.yaml"), []byte(baseKustContent), 0644)
	assert.NoError(t, err)

	// Create component kustomization
	compDir := filepath.Join(rootDir, "component")
	err = os.MkdirAll(compDir, 0755)
	assert.NoError(t, err)

	// Create component kustomization.yaml with kind: Component
	compKustContent := `
kind: Component
patches:
  - path: patches/patch2.yaml
    target:
      kind: Deployment
      name: test
`
	err = os.WriteFile(filepath.Join(compDir, "kustomization.yaml"), []byte(compKustContent), 0644)
	assert.NoError(t, err)

	// Create test resources and patches
	resourceContent := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test
spec:
  replicas: 1
`
	err = os.WriteFile(filepath.Join(baseDir, "resource.yaml"), []byte(resourceContent), 0644)
	assert.NoError(t, err)

	// Create patches
	basePatchDir := filepath.Join(baseDir, "patches")
	err = os.MkdirAll(basePatchDir, 0755)
	assert.NoError(t, err)

	compPatchDir := filepath.Join(compDir, "patches")
	err = os.MkdirAll(compPatchDir, 0755)
	assert.NoError(t, err)

	patch1Content := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test
spec:
  replicas: 2
`
	err = os.WriteFile(filepath.Join(basePatchDir, "patch1.yaml"), []byte(patch1Content), 0644)
	assert.NoError(t, err)

	patch2Content := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test
spec:
  replicas: 3
`
	err = os.WriteFile(filepath.Join(compPatchDir, "patch2.yaml"), []byte(patch2Content), 0644)
	assert.NoError(t, err)

	// Test path resolution
	fs := filesys.MakeFsOnDisk()
	k := krusty.MakeKustomizer(krusty.MakeDefaultOptions())
	allPatches := make([]types.Patch, 0)
	allResources := make(map[string]*resource.Resource)

	processKustomization(fs, k, rootDir, &allPatches, allResources)

	// Verify patches were collected with correct paths
	assert.Equal(t, 2, len(allPatches), "Should collect both patches")

	// Verify base patch path
	basePatchPath := filepath.Join(baseDir, "patches", "patch1.yaml")
	assert.Equal(t, basePatchPath, allPatches[0].Path, "Base patch path should be resolved correctly")

	// Verify component patch path
	compPatchPath := filepath.Join(compDir, "patches", "patch2.yaml")
	assert.Equal(t, compPatchPath, allPatches[1].Path, "Component patch path should be resolved correctly")
}
