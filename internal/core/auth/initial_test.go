package auth

import "testing"

func TestInitialCredentials(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		username, password, err := InitialCredentials("", "")
		if err != nil {
			t.Fatal(err)
		}
		if !ValidUsername(username) || len(username) != 25 || len(password) != 43 {
			t.Fatal("invalid generated credentials")
		}
		if seen[username] || seen[password] {
			t.Fatal("duplicate random credentials")
		}
		seen[username], seen[password] = true, true
	}
	for _, input := range [][2]string{{"custom_admin", "custom-long-password"}, {"custom_admin", ""}, {"", "custom-long-password"}} {
		username, password, err := InitialCredentials(input[0], input[1])
		if err != nil {
			t.Fatal(err)
		}
		if input[0] != "" && username != input[0] || input[1] != "" && password != input[1] {
			t.Fatal("custom credential changed")
		}
	}
	for _, input := range [][2]string{{"bad account", ""}, {"a", ""}, {"custom_admin", "short"}, {"custom_admin", string([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff})}} {
		if _, _, err := InitialCredentials(input[0], input[1]); err == nil {
			t.Fatal("invalid custom credential accepted")
		}
	}
}
