package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/labstack/echo/v5"
	"github.com/spf13/cobra"

	"github.com/anorebel/anorebelcoin/internal/api"
)

// authOptions carries the credential and transport settings shared by the
// devnet and consensus commands.
type authOptions struct {
	oidcIssuer   string
	oidcAudience string
	oidcJWKS     string
	tlsCert      string
	tlsKey       string
	tlsClientCA  string
}

func (o *authOptions) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.oidcIssuer, "oidc-issuer", "", "OIDC issuer URL for RS256 JWT access tokens")
	cmd.Flags().StringVar(&o.oidcAudience, "oidc-audience", "", "required JWT audience")
	cmd.Flags().StringVar(&o.oidcJWKS, "oidc-jwks", "", "static JWKS file with the issuer's RSA signing keys")
	cmd.Flags().StringVar(&o.tlsCert, "tls-cert", "", "TLS certificate chain for the API")
	cmd.Flags().StringVar(&o.tlsKey, "tls-key", "", "TLS private key for the API")
	cmd.Flags().StringVar(&o.tlsClientCA, "tls-client-ca", "",
		"trusted client CA bundle; verified client certificates grant admin")
}

// oidc returns nil when no OIDC flag is set. Setting one requires all three.
func (o *authOptions) oidc() (*api.OIDCConfig, error) {
	if o.oidcIssuer == "" && o.oidcAudience == "" && o.oidcJWKS == "" {
		return nil, nil
	}
	return api.LoadOIDC(o.oidcIssuer, o.oidcAudience, o.oidcJWKS)
}

// adminMTLS reports whether verified client certificates unlock admin routes.
func (o *authOptions) adminMTLS() bool { return o.tlsClientCA != "" }

func (o *authOptions) scheme() string {
	if o.tlsCert != "" {
		return "https"
	}
	return "http"
}

// tlsConfig returns nil when the API should stay on plain HTTP.
func (o *authOptions) tlsConfig() (*tls.Config, error) {
	if o.tlsCert == "" && o.tlsKey == "" && o.tlsClientCA == "" {
		return nil, nil
	}
	if o.tlsCert == "" || o.tlsKey == "" {
		return nil, errors.New("--tls-cert and --tls-key are required together")
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if o.tlsClientCA != "" {
		pem, err := os.ReadFile(o.tlsClientCA)
		if err != nil {
			return nil, fmt.Errorf("--tls-client-ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("--tls-client-ca: %s has no PEM certificates", o.tlsClientCA)
		}
		config.ClientCAs = pool
		config.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return config, nil
}

// serve runs the API with TLS and, when a client CA is configured, mutual TLS.
// A verified client certificate is the admin credential on this listener.
func (o *authOptions) serve(e *echo.Echo, addr string) error {
	config, err := o.tlsConfig()
	if err != nil {
		return err
	}
	if config == nil {
		return e.Start(addr)
	}
	server := &http.Server{Addr: addr, Handler: e, TLSConfig: config}
	return server.ListenAndServeTLS(o.tlsCert, o.tlsKey)
}
