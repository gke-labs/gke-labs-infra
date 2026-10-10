// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"sigs.k8s.io/yaml"
)

// UnmarshalStrict unmarshals YAML data into obj, rejecting unknown fields.
// An unknown key produces an error that names the full key path and,
// if a close match is found, suggests the nearest valid key.
func UnmarshalStrict(data []byte, obj any) error {
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return yaml.UnmarshalStrict(data, obj)
	}
	if raw != nil {
		if rawMap, ok := toMap(raw); ok {
			targetType := reflect.TypeOf(obj)
			if err := validateUnknownKeys(rawMap, targetType, ""); err != nil {
				return err
			}
		}
	}
	return yaml.UnmarshalStrict(data, obj)
}

func validateUnknownKeys(rawMap map[string]any, targetType reflect.Type, parentPath string) error {
	for targetType.Kind() == reflect.Pointer {
		targetType = targetType.Elem()
	}
	if targetType.Kind() != reflect.Struct {
		return nil
	}

	fields, names := getStructFields(targetType)

	var rawKeys []string
	for k := range rawMap {
		rawKeys = append(rawKeys, k)
	}
	sort.Strings(rawKeys)

	for _, k := range rawKeys {
		fullPath := k
		if parentPath != "" {
			fullPath = parentPath + "." + k
		}

		fieldInfo, ok := fields[k]
		if !ok {
			closest := findClosest(k, names)
			if closest != "" {
				return fmt.Errorf("unknown key %q (did you mean %q?)", fullPath, closest)
			}
			sort.Strings(names)
			return fmt.Errorf("unknown key %q (valid keys: %s)", fullPath, strings.Join(names, ", "))
		}

		val := rawMap[k]
		if val == nil {
			continue
		}

		fieldType := fieldInfo.field.Type
		for fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}

		if fieldType.Kind() == reflect.Struct {
			if childMap, ok := toMap(val); ok {
				if err := validateUnknownKeys(childMap, fieldType, fullPath); err != nil {
					return err
				}
			} else {
				subFields, _ := getStructFields(fieldType)
				if _, hasEnabled := subFields["enabled"]; hasEnabled {
					if _, isBool := val.(bool); isBool {
						return fmt.Errorf("invalid value for %q: got boolean, expected a mapping (did you mean %s.enabled: %v?)", fullPath, fullPath, val)
					}
				}
				return fmt.Errorf("invalid value for %q: expected a mapping", fullPath)
			}
		} else if fieldType.Kind() == reflect.Slice {
			elemType := fieldType.Elem()
			for elemType.Kind() == reflect.Pointer {
				elemType = elemType.Elem()
			}
			if elemType.Kind() == reflect.Struct {
				if childSlice, ok := val.([]any); ok {
					for idx, item := range childSlice {
						if itemMap, ok := toMap(item); ok {
							elemPath := fmt.Sprintf("%s[%d]", fullPath, idx)
							if err := validateUnknownKeys(itemMap, elemType, elemPath); err != nil {
								return err
							}
						}
					}
				}
			}
		} else if fieldType.Kind() == reflect.Map {
			elemType := fieldType.Elem()
			for elemType.Kind() == reflect.Pointer {
				elemType = elemType.Elem()
			}
			if elemType.Kind() == reflect.Struct {
				if childMap, ok := toMap(val); ok {
					var childKeys []string
					for ck := range childMap {
						childKeys = append(childKeys, ck)
					}
					sort.Strings(childKeys)
					for _, ck := range childKeys {
						if mapItem, ok := toMap(childMap[ck]); ok {
							mapItemPath := fmt.Sprintf("%s.%s", fullPath, ck)
							if err := validateUnknownKeys(mapItem, elemType, mapItemPath); err != nil {
								return err
							}
						}
					}
				}
			}
		}
	}
	return nil
}

type structFieldInfo struct {
	field reflect.StructField
	name  string
}

func getStructFields(t reflect.Type) (map[string]structFieldInfo, []string) {
	fields := make(map[string]structFieldInfo)
	var names []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" && !f.Anonymous {
			continue
		}
		if f.Anonymous {
			embeddedType := f.Type
			for embeddedType.Kind() == reflect.Pointer {
				embeddedType = embeddedType.Elem()
			}
			if embeddedType.Kind() == reflect.Struct {
				subFields, _ := getStructFields(embeddedType)
				for k, v := range subFields {
					if _, exists := fields[k]; !exists {
						fields[k] = v
						names = append(names, k)
					}
				}
			}
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "" {
			tag = f.Tag.Get("yaml")
		}
		if tag == "-" {
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name == "" {
			name = f.Name
		}
		fields[name] = structFieldInfo{field: f, name: name}
		names = append(names, name)
	}
	return fields, names
}

func toMap(v any) (map[string]any, bool) {
	if m, ok := v.(map[string]any); ok {
		return m, true
	}
	if m, ok := v.(map[any]any); ok {
		res := make(map[string]any, len(m))
		for k, val := range m {
			res[fmt.Sprintf("%v", k)] = val
		}
		return res, true
	}
	return nil, false
}

func findClosest(name string, candidates []string) string {
	bestCandidate := ""
	bestDist := 999
	for _, c := range candidates {
		if strings.EqualFold(name, c) {
			return c
		}
		dist := levenshtein(strings.ToLower(name), strings.ToLower(c))
		if dist < bestDist {
			bestDist = dist
			bestCandidate = c
		}
	}
	if bestDist <= 3 && bestDist < len(name) && bestDist < len(bestCandidate) {
		return bestCandidate
	}
	return ""
}

func levenshtein(a, b string) int {
	la, lb := len(a), len(b)
	d := make([][]int, la+1)
	for i := range d {
		d[i] = make([]int, lb+1)
		d[i][0] = i
	}
	for j := 0; j <= lb; j++ {
		d[0][j] = j
	}
	for i := 1; i <= la; i++ {
		for j := 1; j <= lb; j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
		}
	}
	return d[la][lb]
}
