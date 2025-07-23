//===----------------------------------------------------------------------===//
// Copyright © 2025 Apple Inc. and the container-builder-shim project authors. All rights reserved.
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

package build

import (
	"reflect"
	"strings"
	"testing"

	"github.com/moby/buildkit/client"
)

func TestParseOutput(t *testing.T) {
	tests := []struct {
		name    string
		outputs []string
		want    []client.ExportEntry
		wantErr bool
	}{
		{
			name:    "single oci output",
			outputs: []string{"type=oci,dest=/tmp/image.tar"},
			want: []client.ExportEntry{
				{
					Type: "oci",
					Attrs: map[string]string{
						"dest": "/tmp/image.tar",
					},
				},
			},
			wantErr: false,
		},
		{
			name:    "single tar output",
			outputs: []string{"type=tar,dest=/tmp/app.tar"},
			want: []client.ExportEntry{
				{
					Type: "tar",
					Attrs: map[string]string{
						"dest": "/tmp/app.tar",
					},
				},
			},
			wantErr: false,
		},
		{
			name:    "local output without dest",
			outputs: []string{"type=local"},
			want: []client.ExportEntry{
				{
					Type:  "local",
					Attrs: map[string]string{},
				},
			},
			wantErr: false,
		},
		{
			name:    "local output with dest",
			outputs: []string{"type=local,dest=/tmp"},
			want: []client.ExportEntry{
				{
					Type: "local",
					Attrs: map[string]string{
						"dest": "/tmp",
					},
				},
			},
			wantErr: false,
		},
		{
			name:    "multiple outputs",
			outputs: []string{"type=oci,dest=/tmp/oci.tar", "type=tar,dest=/tmp/app.tar", "type=local"},
			want: []client.ExportEntry{
				{
					Type: "oci",
					Attrs: map[string]string{
						"dest": "/tmp/oci.tar",
					},
				},
				{
					Type: "tar",
					Attrs: map[string]string{
						"dest": "/tmp/app.tar",
					},
				},
				{
					Type:  "local",
					Attrs: map[string]string{},
				},
			},
			wantErr: false,
		},
		{
			name:    "empty outputs",
			outputs: []string{},
			want:    nil,
			wantErr: false,
		},
		{
			name:    "invalid output - no type",
			outputs: []string{"dest=/tmp/image.tar"},
			want:    nil,
			wantErr: true,
		},
		{
			name:    "invalid output - unsupported type",
			outputs: []string{"type=docker,dest=/tmp/image.tar"},
			want:    nil,
			wantErr: true,
		},
		{
			name:    "invalid field format",
			outputs: []string{"type=oci,invalid-field"},
			want:    nil,
			wantErr: true,
		},
		{
			name:    "output with additional attributes",
			outputs: []string{"type=oci,dest=/tmp/image.tar,name=myimage:latest"},
			want: []client.ExportEntry{
				{
					Type: "oci",
					Attrs: map[string]string{
						"dest": "/tmp/image.tar",
						"name": "myimage:latest",
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseOutput(tt.outputs)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseOutput() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return // Don't check result on error
			}
			// For empty slice, check length instead of DeepEqual
			if len(tt.outputs) == 0 && len(got) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseOutput() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseOutputCSV(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		want    client.ExportEntry
		wantErr bool
		errMsg  string
	}{
		{
			name:   "valid oci output",
			output: "type=oci,dest=/tmp/image.tar",
			want: client.ExportEntry{
				Type: "oci",
				Attrs: map[string]string{
					"dest": "/tmp/image.tar",
				},
			},
			wantErr: false,
		},
		{
			name:   "valid tar output",
			output: "type=tar,dest=/tmp/app.tar",
			want: client.ExportEntry{
				Type: "tar",
				Attrs: map[string]string{
					"dest": "/tmp/app.tar",
				},
			},
			wantErr: false,
		},
		{
			name:   "valid local output without dest",
			output: "type=local",
			want: client.ExportEntry{
				Type:  "local",
				Attrs: map[string]string{},
			},
			wantErr: false,
		},
		{
			name:   "valid local output with dest",
			output: "type=local,dest=/tmp/output",
			want: client.ExportEntry{
				Type: "local",
				Attrs: map[string]string{
					"dest": "/tmp/output",
				},
			},
			wantErr: false,
		},
		{
			name:    "no type specified",
			output:  "dest=/tmp/image.tar",
			want:    client.ExportEntry{},
			wantErr: true,
			errMsg:  "output type is required",
		},
		{
			name:    "empty type",
			output:  "type=,dest=/tmp/image.tar",
			want:    client.ExportEntry{},
			wantErr: true,
			errMsg:  "output type is required",
		},
		{
			name:    "unsupported type",
			output:  "type=docker,dest=/tmp/image.tar",
			want:    client.ExportEntry{},
			wantErr: true,
			errMsg:  "unsupported output type: docker",
		},
		{
			name:    "invalid field format",
			output:  "type=oci,invalid-field",
			want:    client.ExportEntry{},
			wantErr: true,
			errMsg:  "invalid field format",
		},
		{
			name:   "spaces in values",
			output: "type=oci, dest=/tmp/image.tar ",
			want: client.ExportEntry{
				Type: "oci",
				Attrs: map[string]string{
					"dest": "/tmp/image.tar",
				},
			},
			wantErr: false,
		},
		{
			name:   "multiple attributes",
			output: "type=oci,dest=/tmp/image.tar,name=myapp:v1.0,annotation.author=test",
			want: client.ExportEntry{
				Type: "oci",
				Attrs: map[string]string{
					"dest":              "/tmp/image.tar",
					"name":              "myapp:v1.0",
					"annotation.author": "test",
				},
			},
			wantErr: false,
		},
		{
			name:   "quoted values with commas",
			output: `type=oci,"dest=/tmp/path,with,commas.tar","name=my,app"`,
			want: client.ExportEntry{
				Type: "oci",
				Attrs: map[string]string{
					"dest": "/tmp/path,with,commas.tar",
					"name": "my,app",
				},
			},
			wantErr: false,
		},
		{
			name:   "case insensitive keys",
			output: "TYPE=oci,DEST=/tmp/image.tar,Name=test",
			want: client.ExportEntry{
				Type: "oci",
				Attrs: map[string]string{
					"dest": "/tmp/image.tar",
					"name": "test",
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseOutputCSV(tt.output)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseOutputCSV() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" && err != nil {
				if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("parseOutputCSV() error = %v, want error containing %v", err, tt.errMsg)
				}
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseOutputCSV() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseOutputCSVInvalidCSV(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		wantErr bool
	}{
		{
			name:    "unmatched quotes",
			output:  `type=oci,dest="/tmp/unclosed`,
			wantErr: true,
		},
		{
			name:    "invalid escape sequence",
			output:  `type=oci,dest="\invalid"`,
			wantErr: true, // csvvalue will error on invalid escape
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseOutputCSV(tt.output)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseOutputCSV() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
