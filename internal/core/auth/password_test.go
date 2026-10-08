package auth

import "testing"

func TestArgon2idUsesReviewableParametersAndRandomSalts(t *testing.T) {
	params := DefaultPasswordParameters
	if params.MemoryKiB < 64*1024 || params.Iterations < 3 || params.Parallelism < 1 || params.SaltLength < 16 || params.KeyLength < 32 {
		t.Fatalf("weak Argon2id parameters: %+v", params)
	}
	password := "correct horse battery staple"
	hash1, salt1, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	hash2, salt2, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if string(salt1) == string(salt2) || string(hash1) == string(hash2) {
		t.Fatal("separate hashes reused a salt")
	}
	if !VerifyPassword(password, hash1, salt1) || VerifyPassword("wrong password", hash1, salt1) {
		t.Fatal("password verification returned an unexpected result")
	}
	if _, _, err := HashPassword("short"); err == nil {
		t.Fatal("short password was accepted")
	}
}
