package vsphereclient

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func Test_parseURL(t *testing.T) {
	tests := []struct {
		name         string
		vsphereUrl   string
		username     string
		password     string
		wantUser     bool
		wantUsername string
		wantPassword string
		wantErr      bool
	}{
		{
			name:       "no username or password",
			vsphereUrl: "https://example.com/sdk",
			username:   "",
			password:   "",
			wantUser:   false,
			wantErr:    false,
		},
		{
			name:         "username and password in original url",
			vsphereUrl:   "https://origuser:origpass@example.com/sdk",
			username:     "",
			password:     "",
			wantUser:     true,
			wantUsername: "origuser",
			wantPassword: "origpass",
			wantErr:      false,
		},
		{
			name:         "overriden username and password in original url",
			vsphereUrl:   "https://origuser:origpass@example.com/sdk",
			username:     "user",
			password:     "pass",
			wantUser:     true,
			wantUsername: "user",
			wantPassword: "pass",
			wantErr:      false,
		},
		{
			name:         "with username and password",
			vsphereUrl:   "https://example.com/sdk",
			username:     "user",
			password:     "pass",
			wantUser:     true,
			wantUsername: "user",
			wantPassword: "pass",
			wantErr:      false,
		},
		{
			name:         "with special chars in username and password",
			vsphereUrl:   "https://example.com/sdk",
			username:     "user@domain.com",
			password:     "p@ss/w:rd",
			wantUser:     true,
			wantUsername: "user@domain.com",
			wantPassword: "p@ss/w:rd",
			wantErr:      false,
		},
		{
			name:       "username but empty password",
			vsphereUrl: "https://example.com/sdk",
			username:   "user",
			password:   "",
			wantUser:   false,
			wantErr:    false,
		},
		{
			name:       "invalid url",
			vsphereUrl: "://bad_url",
			username:   "user",
			password:   "pass",
			wantUser:   false,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseURL(tt.vsphereUrl, tt.username, tt.password)
			if tt.wantErr {
				require.Error(t, err, "expected error")
				return
			}
			require.NoError(t, err, "unexpected error")
			if tt.wantUser {
				require.NotNil(t, got.User, "expected user info")
				username := got.User.Username()
				password, _ := got.User.Password()
				require.Equal(t, tt.wantUsername, username, "username mismatch")
				require.Equal(t, tt.wantPassword, password, "password mismatch")
			} else {
				require.Nil(t, got.User, "expected no user info")
			}
			// Ensure the returned URL parses as expected
			_, err = url.Parse(got.String())
			require.NoError(t, err, "returned URL is not valid")
		})
	}
}
