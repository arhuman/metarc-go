package main

import "testing"

func TestResolveZstdWindow(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    int
		wantErr bool
	}{
		{name: "unset means defer to the solid block size", in: "", want: 0},
		{name: "blank is treated as unset", in: "   ", want: 0},
		{name: "power of two in MB", in: "32MB", want: 32 << 20},
		{name: "raw byte count", in: "1048576", want: 1 << 20},
		{name: "not a power of two", in: "20MB", wantErr: true},
		{name: "below the library minimum", in: "512", wantErr: true},
		{name: "above the library maximum", in: "1GB", wantErr: true},
		{name: "unparseable", in: "big", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveZstdWindow(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveZstdWindow(%q) = %d, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveZstdWindow(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("resolveZstdWindow(%q) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
