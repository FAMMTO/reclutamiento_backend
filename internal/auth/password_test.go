package auth

import "testing"

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("MiContraseña123")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	ok, err := VerifyPassword("MiContraseña123", hash)
	if err != nil || !ok {
		t.Fatalf("la contraseña correcta no verificó: ok=%v err=%v", ok, err)
	}

	ok, err = VerifyPassword("otraContraseña", hash)
	if err != nil {
		t.Fatalf("VerifyPassword con contraseña incorrecta devolvió error: %v", err)
	}
	if ok {
		t.Fatal("una contraseña incorrecta verificó como válida")
	}
}

func TestHashesAreSalted(t *testing.T) {
	h1, _ := HashPassword("misma-contraseña-1")
	h2, _ := HashPassword("misma-contraseña-1")
	if h1 == h2 {
		t.Fatal("dos hashes de la misma contraseña son idénticos: falta salt aleatorio")
	}
}

func TestVerifyRejectsMalformedHash(t *testing.T) {
	if _, err := VerifyPassword("x", "$2a$10$bcrypt-no-soportado"); err == nil {
		t.Fatal("un hash con formato desconocido debe devolver error")
	}
}

func TestValidateNewPassword(t *testing.T) {
	cases := []struct {
		password string
		valid    bool
	}{
		{"corta1", false},          // < 10
		{"sololetrasaqui", false},  // sin dígitos
		{"1234567890123", false},   // sin letras
		{"Password123", true},
		{"otraClave2026", true},
	}
	for _, tc := range cases {
		err := ValidateNewPassword(tc.password)
		if tc.valid && err != nil {
			t.Errorf("%q debería ser válida: %v", tc.password, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("%q debería ser rechazada", tc.password)
		}
	}
}
