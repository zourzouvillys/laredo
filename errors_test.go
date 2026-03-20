package laredo

import "testing"

func TestValidationError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  ValidationError
		want string
	}{
		{
			name: "with table",
			err:  ValidationError{Table: &TableIdentifier{Schema: "public", Table: "users"}, Code: "TABLE_NOT_FOUND", Message: "table does not exist"},
			want: "public.users: TABLE_NOT_FOUND: table does not exist",
		},
		{
			name: "without table",
			err:  ValidationError{Table: nil, Code: "PERMISSION_DENIED", Message: "insufficient privileges"},
			want: "PERMISSION_DENIED: insufficient privileges",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}
