//go:build windows

package config

import "testing"

func TestDefaultPath(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr error
	}{
		{
			name: "drive-qualified LOCALAPPDATA wins",
			env:  map[string]string{"LOCALAPPDATA": `C:\Users\Alice\AppData\Local`},
			want: `C:\Users\Alice\AppData\Local\AI CLI Gateway\config\config.toml`,
		},
		{
			name:    "empty LOCALAPPDATA returns sentinel",
			env:     map[string]string{"LOCALAPPDATA": ""},
			wantErr: ErrDefaultPath,
		},
		{
			name:    "drive-relative LOCALAPPDATA returns sentinel",
			env:     map[string]string{"LOCALAPPDATA": `C:dir`},
			wantErr: ErrDefaultPath,
		},
		{
			name:    "rooted volume-less LOCALAPPDATA returns sentinel",
			env:     map[string]string{"LOCALAPPDATA": `\dir`},
			wantErr: ErrDefaultPath,
		},
		{
			name:    "UNC LOCALAPPDATA returns sentinel",
			env:     map[string]string{"LOCALAPPDATA": `\\server\share\dir`},
			wantErr: ErrDefaultPath,
		},
		{
			name:    "device LOCALAPPDATA returns sentinel",
			env:     map[string]string{"LOCALAPPDATA": `\\?\C:\dir`},
			wantErr: ErrDefaultPath,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := defaultPath(lookupEnv(test.env))
			if err != test.wantErr {
				t.Fatalf("defaultPath() error = %v, want %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("defaultPath() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDefaultInitRuntimeRoot(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    string
		wantErr error
	}{
		{
			name: "drive-qualified LOCALAPPDATA wins",
			env:  map[string]string{"LOCALAPPDATA": `C:\Users\Alice\AppData\Local`},
			want: `C:\Users\Alice\AppData\Local\AI CLI Gateway\runtime`,
		},
		{
			name:    "empty LOCALAPPDATA returns sentinel",
			env:     map[string]string{"LOCALAPPDATA": ""},
			wantErr: ErrDefaultPath,
		},
		{
			name:    "drive-relative LOCALAPPDATA returns sentinel",
			env:     map[string]string{"LOCALAPPDATA": `C:dir`},
			wantErr: ErrDefaultPath,
		},
		{
			name:    "rooted volume-less LOCALAPPDATA returns sentinel",
			env:     map[string]string{"LOCALAPPDATA": `\dir`},
			wantErr: ErrDefaultPath,
		},
		{
			name:    "UNC LOCALAPPDATA returns sentinel",
			env:     map[string]string{"LOCALAPPDATA": `\\server\share\dir`},
			wantErr: ErrDefaultPath,
		},
		{
			name:    "device LOCALAPPDATA returns sentinel",
			env:     map[string]string{"LOCALAPPDATA": `\\.\C:\dir`},
			wantErr: ErrDefaultPath,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := defaultInitRuntimeRoot(lookupEnv(test.env))
			if err != test.wantErr {
				t.Fatalf("defaultInitRuntimeRoot() error = %v, want %v", err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("defaultInitRuntimeRoot() = %q, want %q", got, test.want)
			}
		})
	}
}

func lookupEnv(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
