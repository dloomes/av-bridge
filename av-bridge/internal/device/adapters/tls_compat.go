package adapters

import "crypto/tls"

// deviceTLSConfig is the TLS client config for HTTPS connections to AV
// devices. Go's defaults have moved past what a lot of AV firmware speaks,
// and a device that shares nothing with the client answers with a bare
// "tls: handshake failure":
//
//   - Since Go 1.22, cipher suites using RSA key exchange aren't offered
//     by default; codecs and displays whose certificates only work with
//     them (common on older Cisco CE / RoomOS and Poly firmware) refuse
//     every connection.
//   - Since Go 1.24, the ClientHello offers the post-quantum X25519MLKEM768
//     key share, which some embedded TLS stacks reject outright.
//
// So device connections offer the RSA key-exchange suites after the
// modern ones, and only classic key exchange groups. The hop is on the
// site network, to a device that usually has a self-signed certificate
// (skipVerify, opt-in per device), so this trades nothing that matters
// for being able to talk to the device at all. TLS 1.3 suites aren't
// configurable and are always offered.
func deviceTLSConfig(skipVerify bool) *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: skipVerify, //nolint:gosec // opt-in per device, for self-signed certificates
		MinVersion:         tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			// RSA key exchange, for older device firmware.
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		},
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384, tls.CurveP521},
	}
}

// legacyDeviceTLSConfig is deviceTLSConfig for device firmware whose web
// server predates TLS 1.2 (Cisco TC, for one): TLS 1.0 and 1.1 are
// accepted too, with 3DES as a last resort. Only for adapters that need it.
func legacyDeviceTLSConfig(skipVerify bool) *tls.Config {
	c := deviceTLSConfig(skipVerify)
	c.MinVersion = tls.VersionTLS10                                            //nolint:gosec // device firmware that predates TLS 1.2
	c.CipherSuites = append(c.CipherSuites, tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA) //nolint:gosec // as above
	return c
}
