package auth

import (
	"strings"
	"testing"
)

// Jellyfin hashes. The vectors were computed separately with Python's hashlib.pbkdf2_hmac.
func TestLegacyJellyfinHashes(t *testing.T) {
	const (
		sha512Hash = "$PBKDF2-SHA512$iterations=1000$8C1F0D2E3A4B5C6D7E8F90A1B2C3D4E5F60718293A4B5C6D7E8F9012345678AB$" +
			"E4E035978C80256F9F1565845F54B83D2D4B6975B03A6F09A7FF935F38A202C69C30C9BD59436C461B1FE7E4143BDE21CECA03935F951764A89EB6DB72410C97"
		sha1Hash = "$PBKDF2$iterations=1000$00112233445566778899AABBCCDDEEFF$7C5444D7CE94931467CCA498E5DBA6AAB613A99C"
	)
	for _, c := range []struct {
		hash, good string
	}{{sha512Hash, "mot de passe é"}, {sha1Hash, "ancien"}} {
		if !IsLegacyHash(c.hash) {
			t.Errorf("%s... not recognized", c.hash[:20])
		}
		ok, rehash, err := VerifyPassword(c.hash, c.good)
		if !ok || !rehash || err != nil {
			t.Errorf("%s...: right password rejected (%v %v %v)", c.hash[:20], ok, rehash, err)
		}
		if ok, rehash, err := VerifyPassword(c.hash, c.good+"x"); ok || rehash || err != nil {
			t.Errorf("%s...: wrong password accepted (%v %v %v)", c.hash[:20], ok, rehash, err)
		}
		// The algorithm name is upper case.
		if ok, _, _ := VerifyPassword(strings.Replace(c.hash, "PBKDF2", "pbkdf2", 1), c.good); ok {
			t.Errorf("%s...: lower-case algorithm name accepted", c.hash[:20])
		}
	}
	for _, bad := range []string{
		"$PBKDF2-SHA512$iterations=999$00$00112233445566778899AABBCCDDEEFF",     // too few iterations
		"$PBKDF2-SHA512$iterations=5000000$00$00112233445566778899AABBCCDDEEFF", // far too many
		"$PBKDF2-SHA512$iterations=1000$$00112233445566778899AABBCCDDEEFF",      // no salt
		"$PBKDF2-SHA512$iterations=1000$00$0011",                                // hash too short
		"$PBKDF2-MD5$iterations=1000$00$00112233445566778899AABBCCDDEEFF",       // unknown algorithm
		"$PBKDF2-SHA512$rounds=1000$00$00112233445566778899AABBCCDDEEFF",        // unknown parameter
		"$PBKDF2-SHA512$iterations=1000$zz$00112233445566778899AABBCCDDEEFF",    // not hex
	} {
		if IsLegacyHash(bad) {
			t.Errorf("%q recognized", bad)
		}
		if ok, _, err := VerifyPassword(bad, "x"); ok || err == nil {
			t.Errorf("%q: %v %v", bad, ok, err)
		}
	}
}
