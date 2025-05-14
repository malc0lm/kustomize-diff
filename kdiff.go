package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"flag"

	"github.com/r3labs/diff/v3"
	"sigs.k8s.io/kustomize/api/filesys"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/resource"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/yaml"
)

// FieldSource tracks where a field value came from
type FieldSource struct {
	Resource string   // The resource being modified
	Path     []string // The field path that changed
	Source   string   // The patch file that caused the change
	Original interface{}
	New      interface{}
}

var fieldSources []FieldSource

func main() {
	// Define command line flags
	var showFinalOutput bool
	flag.BoolVar(&showFinalOutput, "show-final", false, "Show the final kustomize output")
	flag.Parse()

	// Check if we have the required kustomization directory argument
	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "Usage: %s [-show-final] <kustomization-dir>\n", os.Args[0])
		os.Exit(1)
	}

	kustomizationDir := flag.Arg(0)
	fs := filesys.MakeFsOnDisk()

	// 1. Build the final kustomization
	opts := krusty.MakeDefaultOptions()
	k := krusty.MakeKustomizer(opts)
	finalResMap, err := k.Run(fs, kustomizationDir)
	if err != nil {
		logFatal("Kustomize build failed: %v", err)
	}

	// 2. Load kustomization.yaml
	kustData, err := fs.ReadFile(filepath.Join(kustomizationDir, "kustomization.yaml"))
	if err != nil {
		logFatal("Failed reading kustomization.yaml: %v", err)
	}

	var kust types.Kustomization
	if err := yaml.Unmarshal(kustData, &kust); err != nil {
		logFatal("Failed parsing kustomization.yaml: %v", err)
	}

	// Debug kustomization content
	fmt.Printf("\n=== Kustomization Configuration ===\n")
	fmt.Printf("Base Resources:\n")
	for _, res := range kust.Resources {
		fmt.Printf("  - %s\n", res)
	}
	if len(kust.Components) > 0 {
		fmt.Printf("Components:\n")
		for _, comp := range kust.Components {
			fmt.Printf("  - %s\n", comp)
		}
	}

	// 3. Recursively collect all patches and resources
	allPatches := make([]types.Patch, 0)
	allResources := make(map[string]*resource.Resource)
	baseK := krusty.MakeKustomizer(opts)

	// Process each base resource directory
	for _, baseDir := range kust.Resources {
		absBaseDir := filepath.Join(kustomizationDir, baseDir)
		processResourceOrKustomization(fs, baseK, absBaseDir, &allPatches, allResources)
	}

	// Process each component directory
	for _, compDir := range kust.Components {
		absCompDir := filepath.Join(kustomizationDir, compDir)
		processResourceOrKustomization(fs, baseK, absCompDir, &allPatches, allResources)
	}

	// Add inline patches from the root kustomization
	for _, patch := range kust.Patches {
		if patch.Path != "" {
			// Make path relative to root kustomization
			patch.Path = filepath.Join(kustomizationDir, string(patch.Path))
		}
		allPatches = append(allPatches, patch)
	}

	// Add JSON patches from the root kustomization
	for _, patch := range kust.PatchesJson6902 {
		if patch.Path != "" {
			// Make path relative to root kustomization
			patch.Path = filepath.Join(kustomizationDir, string(patch.Path))
		}
		allPatches = append(allPatches, types.Patch{
			Target: patch.Target,
			Patch:  string(patch.Patch),
		})
	}

	fmt.Printf("\nPatches:\n")
	for i, patch := range allPatches {
		if patch.Path != "" {
			fmt.Printf("  %d. File: %s\n", i+1, patch.Path)
		} else {
			fmt.Printf("  %d. Inline Patch\n", i+1)
		}
		fmt.Printf("     Target: %s/%s\n", patch.Target.Kind, patch.Target.Name)
	}

	fmt.Printf("\n=== Processing Patches ===\n")
	fmt.Printf("Found %d base resources\n", len(allResources))

	// 4. Process all collected patches
	fmt.Printf("Found %d patches to apply\n", len(allPatches))
	for i, patch := range allPatches {
		fmt.Printf("\n--- Processing Patch %d/%d ---\n", i+1, len(allPatches))
		if patch.Path != "" {
			fmt.Printf("Patch File: %s\n", patch.Path)
		} else {
			fmt.Printf("Inline Patch\n")
		}
		fmt.Printf("Target: %s/%s\n", patch.Target.Kind, patch.Target.Name)

		// Find target resource
		targetKey := fmt.Sprintf("%s/%s", patch.Target.Kind, patch.Target.Name)
		var targetRes *resource.Resource
		var exists bool
		if patch.Target.Name == "" {
			// If no name specified, find first resource of this kind
			for key, res := range allResources {
				if strings.HasPrefix(key, patch.Target.Kind+"/") {
					targetRes = res
					exists = true
					break
				}
			}
		} else {
			targetRes, exists = allResources[targetKey]
		}
		if !exists {
			fmt.Printf("Warning: No matching resource found for patch target\n")
			continue
		}

		// Get state before patch
		var beforeMap map[string]interface{}
		if err := yaml.Unmarshal([]byte(targetRes.MustYaml()), &beforeMap); err != nil {
			logFatal("Failed to unmarshal before state: %v", err)
		}

		// Create a copy of the base resource for patching
		patchedRes := targetRes.DeepCopy()

		// Apply patch
		var patchData []byte
		if patch.Path != "" {
			// File-based patch
			var err error
			patchData, err = fs.ReadFile(patch.Path)
			if err != nil {
				fmt.Printf("Warning: Reading patch %s failed: %v\n", patch.Path, err)
				continue
			}
		} else {
			// Inline patch
			patchData = []byte(patch.Patch)
		}

		// Parse the patch data
		var patchContent interface{}
		if err := yaml.Unmarshal(patchData, &patchContent); err != nil {
			fmt.Printf("Warning: Failed to parse patch content: %v\n", err)
			continue
		}

		// Convert the resource to a map for patching
		var resourceMap map[string]interface{}
		if err := yaml.Unmarshal([]byte(patchedRes.MustYaml()), &resourceMap); err != nil {
			logFatal("Failed to unmarshal resource: %v", err)
		}

		// Apply the patch based on its type
		switch patchContent := patchContent.(type) {
		case []interface{}:
			// JSON patch format
			for _, op := range patchContent {
				opMap, ok := op.(map[string]interface{})
				if !ok {
					logFatal("Invalid patch operation format")
				}
				opType, ok := opMap["op"].(string)
				if !ok {
					logFatal("Missing or invalid operation type")
				}
				path, ok := opMap["path"].(string)
				if !ok {
					logFatal("Missing or invalid path")
				}
				value := opMap["value"]

				// Convert path to array of keys
				pathKeys := parsePath(path)

				// Get original value before change
				originalValue := getValueAtPath(resourceMap, pathKeys)

				// Apply the operation
				switch opType {
				case "add":
					applyAdd(resourceMap, pathKeys, value)
					// Record the change
					fieldSources = append(fieldSources, FieldSource{
						Resource: fmt.Sprintf("%s/%s", targetRes.GetKind(), targetRes.GetName()),
						Path:     pathKeys,
						Source:   patch.Path,
						Original: originalValue,
						New:      value,
					})
				case "replace":
					applyReplace(resourceMap, pathKeys, value)
					// Record the change
					fieldSources = append(fieldSources, FieldSource{
						Resource: fmt.Sprintf("%s/%s", targetRes.GetKind(), targetRes.GetName()),
						Path:     pathKeys,
						Source:   patch.Path,
						Original: originalValue,
						New:      value,
					})
				case "remove":
					applyRemove(resourceMap, pathKeys)
					// Record the removal
					fieldSources = append(fieldSources, FieldSource{
						Resource: fmt.Sprintf("%s/%s", targetRes.GetKind(), targetRes.GetName()),
						Path:     pathKeys,
						Source:   patch.Path,
						Original: originalValue,
						New:      nil,
					})
				}
			}
		case map[string]interface{}:
			// Strategic merge patch format
			// Get original state before merge
			originalState := make(map[string]interface{})
			for k, v := range resourceMap {
				originalState[k] = deepCopyValue(v)
			}

			// Apply the merge
			mergeMap(resourceMap, patchContent)

			// Compare and record changes
			for k, newVal := range resourceMap {
				oldVal, exists := originalState[k]
				if !exists || !reflect.DeepEqual(oldVal, newVal) {
					fieldSources = append(fieldSources, FieldSource{
						Resource: fmt.Sprintf("%s/%s", targetRes.GetKind(), targetRes.GetName()),
						Path:     []string{k},
						Source:   patch.Path,
						Original: oldVal,
						New:      newVal,
					})
				}
			}
			// Check for removed fields
			for k, oldVal := range originalState {
				if _, exists := resourceMap[k]; !exists {
					fieldSources = append(fieldSources, FieldSource{
						Resource: fmt.Sprintf("%s/%s", targetRes.GetKind(), targetRes.GetName()),
						Path:     []string{k},
						Source:   patch.Path,
						Original: oldVal,
						New:      nil,
					})
				}
			}
		}

		// Convert back to YAML
		patchedYaml, err := yaml.Marshal(resourceMap)
		if err != nil {
			logFatal("Failed to marshal patched resource: %v", err)
		}

		// Create new resource from patched YAML
		patchedRes, err = resource.NewFactory(nil).FromBytes(patchedYaml)
		if err != nil {
			logFatal("Failed to create patched resource: %v", err)
		}

		// Get state after patch
		var afterMap map[string]interface{}
		if err := yaml.Unmarshal([]byte(patchedRes.MustYaml()), &afterMap); err != nil {
			logFatal("Failed to unmarshal after state: %v", err)
		}

		// Track changes
		changelog, err := diff.Diff(beforeMap, afterMap)
		if err != nil {
			logFatal("Failed to diff states: %v", err)
		}

		fmt.Printf("Changes detected: %d\n", len(changelog))
	}

	// 5. Output results
	yml, err := finalResMap.AsYaml()
	if err != nil {
		logFatal("Marshal final output failed: %v", err)
	}

	// Print field sources
	fmt.Printf("\n=== Field Changes ===\n")

	// Group changes by resource
	resourceChanges := make(map[string][]FieldSource)
	for _, source := range fieldSources {
		resourceChanges[source.Resource] = append(resourceChanges[source.Resource], source)
	}

	// Print changes grouped by resource
	for resource, changes := range resourceChanges {
		fmt.Printf("\nResource: %s\n", resource)
		fmt.Printf("Changes:\n")
		for _, change := range changes {
			// Format the path in a more readable way
			pathStr := strings.Join(change.Path, " → ")

			// Format the source file name only (without full path)
			sourceFile := change.Source
			if sourceFile != "" {
				sourceFile = filepath.Base(sourceFile)
			} else {
				sourceFile = "inline patch"
			}

			fmt.Printf("  • Field: %s\n", pathStr)
			fmt.Printf("    Modified by: %s\n", sourceFile)

			// Format the values in a more readable way
			if change.Original != nil {
				fmt.Printf("    Original: %v\n", change.Original)
			}
			if change.New != nil {
				fmt.Printf("    New: %v\n", change.New)
			} else {
				fmt.Printf("    Removed\n")
			}
		}
	}

	// Only show final output if flag is set
	if showFinalOutput {
		fmt.Printf("\n=== Final Output ===\n")
		fmt.Println(string(yml))
	}
}

func processResourceOrKustomization(fs filesys.FileSystem, k *krusty.Kustomizer, path string, allPatches *[]types.Patch, allResources map[string]*resource.Resource) {
	// Check if it's a kustomization directory
	kustPath := filepath.Join(path, "kustomization.yaml")
	if _, err := fs.ReadFile(kustPath); err == nil {
		// It's a kustomization directory
		processKustomization(fs, k, path, allPatches, allResources)
		return
	}

	// Try to load as a resource file
	if data, err := fs.ReadFile(path); err == nil {
		// Load the resource
		res, err := resource.NewFactory(nil).FromBytes(data)
		if err != nil {
			logFatal("Failed to load resource %s: %v", path, err)
		}

		// Add to resources map
		key := fmt.Sprintf("%s/%s", res.GetKind(), res.GetName())
		allResources[key] = res
	} else {
		logFatal("Path %s is neither a kustomization directory nor a resource file: %v", path, err)
	}
}

func processKustomization(fs filesys.FileSystem, k *krusty.Kustomizer, dir string, allPatches *[]types.Patch, allResources map[string]*resource.Resource) {
	// Load kustomization.yaml
	kustPath := filepath.Join(dir, "kustomization.yaml")
	kustData, err := fs.ReadFile(kustPath)
	if err != nil {
		logFatal("Failed reading kustomization.yaml at %s: %v", dir, err)
	}

	var kust types.Kustomization
	if err := yaml.Unmarshal(kustData, &kust); err != nil {
		logFatal("Failed parsing kustomization.yaml at %s: %v", dir, err)
	}

	// Add patches from this kustomization, with paths relative to this kustomization
	for _, patch := range kust.Patches {
		if patch.Path != "" {
			// Make path relative to this kustomization
			patch.Path = filepath.Join(dir, string(patch.Path))
		}
		*allPatches = append(*allPatches, patch)
	}

	// Add JSON patches from this kustomization
	for _, patch := range kust.PatchesJson6902 {
		if patch.Path != "" {
			// Make path relative to this kustomization
			patch.Path = filepath.Join(dir, string(patch.Path))
		}
		*allPatches = append(*allPatches, types.Patch{
			Target: patch.Target,
			Patch:  string(patch.Patch),
		})
	}

	// Process resources
	for _, baseDir := range kust.Resources {
		absBaseDir := filepath.Join(dir, baseDir)
		processResourceOrKustomization(fs, k, absBaseDir, allPatches, allResources)
	}

	// Process components
	for _, compDir := range kust.Components {
		absCompDir := filepath.Join(dir, compDir)
		processResourceOrKustomization(fs, k, absCompDir, allPatches, allResources)
	}

	// Build resources from this kustomization last
	resMap, err := k.Run(fs, dir)
	if err != nil {
		logFatal("Base build failed for %s: %v", dir, err)
	}

	// Add resources to our map
	for _, res := range resMap.Resources() {
		key := fmt.Sprintf("%s/%s", res.GetKind(), res.GetName())
		allResources[key] = res
	}
}

func parsePath(path string) []string {
	// Remove leading slash and split by slashes
	if len(path) > 0 && path[0] == '/' {
		path = path[1:]
	}
	return strings.Split(path, "/")
}

func getValueAtPath(m interface{}, path []string) interface{} {
	if len(path) == 0 {
		return m
	}

	key := path[0]
	switch m := m.(type) {
	case map[string]interface{}:
		if val, exists := m[key]; exists {
			return getValueAtPath(val, path[1:])
		}
	case []interface{}:
		if idx, err := strconv.Atoi(key); err == nil && idx >= 0 && idx < len(m) {
			return getValueAtPath(m[idx], path[1:])
		}
	}
	return nil
}

func setValueAtPath(m interface{}, path []string, value interface{}) {
	if len(path) == 0 {
		return
	}

	key := path[0]
	if len(path) == 1 {
		switch m := m.(type) {
		case map[string]interface{}:
			m[key] = value
		case []interface{}:
			if idx, err := strconv.Atoi(key); err == nil && idx >= 0 && idx < len(m) {
				m[idx] = value
			}
		}
		return
	}

	switch m := m.(type) {
	case map[string]interface{}:
		if _, exists := m[key]; !exists {
			m[key] = make(map[string]interface{})
		}
		setValueAtPath(m[key], path[1:], value)
	case []interface{}:
		if idx, err := strconv.Atoi(key); err == nil && idx >= 0 && idx < len(m) {
			setValueAtPath(m[idx], path[1:], value)
		}
	}
}

func applyAdd(m interface{}, path []string, value interface{}) {
	if len(path) == 0 {
		return
	}

	key := path[0]
	if len(path) == 1 {
		switch m := m.(type) {
		case map[string]interface{}:
			m[key] = value
		case []interface{}:
			if idx, err := strconv.Atoi(key); err == nil {
				if idx == -1 {
					m = append(m, value)
				} else if idx >= 0 && idx <= len(m) {
					m = append(m[:idx], append([]interface{}{value}, m[idx:]...)...)
				}
			}
		}
		return
	}

	switch m := m.(type) {
	case map[string]interface{}:
		if _, exists := m[key]; !exists {
			m[key] = make(map[string]interface{})
		}
		applyAdd(m[key], path[1:], value)
	case []interface{}:
		if idx, err := strconv.Atoi(key); err == nil && idx >= 0 && idx < len(m) {
			applyAdd(m[idx], path[1:], value)
		}
	}
}

func applyReplace(m interface{}, path []string, value interface{}) {
	if len(path) == 0 {
		return
	}

	key := path[0]
	if len(path) == 1 {
		switch m := m.(type) {
		case map[string]interface{}:
			m[key] = value
		case []interface{}:
			if idx, err := strconv.Atoi(key); err == nil && idx >= 0 && idx < len(m) {
				m[idx] = value
			}
		}
		return
	}

	switch m := m.(type) {
	case map[string]interface{}:
		if _, exists := m[key]; !exists {
			m[key] = make(map[string]interface{})
		}
		applyReplace(m[key], path[1:], value)
	case []interface{}:
		if idx, err := strconv.Atoi(key); err == nil && idx >= 0 && idx < len(m) {
			applyReplace(m[idx], path[1:], value)
		}
	}
}

func applyRemove(m interface{}, path []string) {
	if len(path) == 0 {
		return
	}

	key := path[0]
	if len(path) == 1 {
		switch m := m.(type) {
		case map[string]interface{}:
			delete(m, key)
		case []interface{}:
			if idx, err := strconv.Atoi(key); err == nil && idx >= 0 && idx < len(m) {
				m = append(m[:idx], m[idx+1:]...)
			}
		}
		return
	}

	switch m := m.(type) {
	case map[string]interface{}:
		if _, exists := m[key]; exists {
			applyRemove(m[key], path[1:])
		}
	case []interface{}:
		if idx, err := strconv.Atoi(key); err == nil && idx >= 0 && idx < len(m) {
			applyRemove(m[idx], path[1:])
		}
	}
}

func mergeMap(dst, src map[string]interface{}) {
	for key, srcVal := range src {
		if dstVal, exists := dst[key]; exists {
			switch srcVal := srcVal.(type) {
			case map[string]interface{}:
				if dstVal, ok := dstVal.(map[string]interface{}); ok {
					mergeMap(dstVal, srcVal)
					continue
				}
			case []interface{}:
				if dstVal, ok := dstVal.([]interface{}); ok {
					dst[key] = append(dstVal, srcVal...)
					continue
				}
			}
		}
		dst[key] = srcVal
	}
}

func deepCopyValue(v interface{}) interface{} {
	switch v := v.(type) {
	case map[string]interface{}:
		newMap := make(map[string]interface{})
		for k, val := range v {
			newMap[k] = deepCopyValue(val)
		}
		return newMap
	case []interface{}:
		newSlice := make([]interface{}, len(v))
		for i, val := range v {
			newSlice[i] = deepCopyValue(val)
		}
		return newSlice
	default:
		return v
	}
}

func logFatal(format string, v ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", v...)
	os.Exit(1)
}
