// Package platform is the shared service plumbing for the CasperVPN monorepo:
// strict env parsing (envcfg), outbound HTTP with bearer auth, retries and
// response-size limits (httpx), and uniform JSON responses (httpjson).
//
// Unlike packages/contracts (frozen wire types), platform is ordinary shared
// code: change it freely, but remember every service links it — keep it free of
// service-specific logic and of any network/domain/IP literals ([АНТИ-БЛОК]).
package platform
