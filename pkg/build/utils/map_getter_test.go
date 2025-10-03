//===----------------------------------------------------------------------===//
// Copyright © 2025 Apple Inc. and the container-builder-shim project authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//===----------------------------------------------------------------------===//

package utils

import (
	"reflect"
	"testing"
)

func TestNewMapGetter(t *testing.T) {
	m := map[string]string{
		"key1": "value1",
		"key2": "value2",
	}

	getter := NewMapGetter(m)

	// Verify the getter is not nil
	if getter == nil {
		t.Fatal("NewMapGetter returned nil")
	}

	// The type should implement MapGetter
	if _, ok := getter.(MapGetter); !ok {
		t.Fatal("NewMapGetter does not return a MapGetter interface")
	}
}

func TestMapGetter_Keys(t *testing.T) {
	tests := []struct {
		name string
		m    map[string]string
		want []string
	}{
		{
			name: "empty map",
			m:    map[string]string{},
			want: []string{},
		},
		{
			name: "single key",
			m:    map[string]string{"key": "value"},
			want: []string{"key"},
		},
		{
			name: "multiple keys",
			m:    map[string]string{"b": "value2", "a": "value1", "c": "value3"},
			want: []string{"a", "b", "c"}, // Keys should be sorted
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getter := NewMapGetter(tt.m)
			got := getter.Keys()

			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Keys() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMapGetter_Get(t *testing.T) {
	m := map[string]string{
		"key1":  "value1",
		"key2":  "value2",
		"empty": "",
	}

	getter := NewMapGetter(m)

	tests := []struct {
		name      string
		key       string
		wantValue string
		wantFound bool
	}{
		{
			name:      "existing key",
			key:       "key1",
			wantValue: "value1",
			wantFound: true,
		},
		{
			name:      "another existing key",
			key:       "key2",
			wantValue: "value2",
			wantFound: true,
		},
		{
			name:      "empty string value",
			key:       "empty",
			wantValue: "",
			wantFound: true,
		},
		{
			name:      "nonexistent key",
			key:       "nonexistent",
			wantValue: "",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotValue, gotFound := getter.Get(tt.key)

			if gotValue != tt.wantValue {
				t.Errorf("Get() value = %v, want %v", gotValue, tt.wantValue)
			}

			if gotFound != tt.wantFound {
				t.Errorf("Get() found = %v, want %v", gotFound, tt.wantFound)
			}
		})
	}
}
