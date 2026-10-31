package cloud

import (
	"crypto/ed25519"
	"regexp"
	"strconv"
	"strings"
	"time"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

var (
	bearerRe  = regexp.MustCompile(`(?i)^Bearer\s+(.+)$`)
	installRe = regexp.MustCompile(`(?i)^Bearer\s+(opi_[A-Za-z0-9_-]+)$`)
)

// VerifyHuman: Authorization: Bearer <ID token> + accounts/{uid}.
func VerifyHuman(authorization string) (*Account, error) {
	m := bearerRe.FindStringSubmatch(authorization)
	if m == nil || strings.HasPrefix(m[1], "opi_") {
		return nil, E.Unauth("missing Authorization: Bearer <Firebase ID token>")
	}
	dec, err := AuthAdmin().VerifyIDToken(m[1], true)
	if err != nil {
		code := driver.AuthErrorCode(err)
		if code == "auth/user-disabled" {
			return nil, E.Forbidden("account disabled")
		}
		if code == "" {
			code = "verify failed"
		}
		return nil, E.Unauth("invalid ID token (" + code + ")")
	}
	snap, err := Col.Accounts().Doc(dec.UID).Get()
	if err != nil {
		return nil, err
	}
	// a signed-in person without a usable account: 403, still audited with this actor
	if !snap.Exists() {
		e := E.Forbidden("no account")
		e.AuditActor = &AuditActor{dec.UID, dec.Email}
		return nil, e
	}
	acc := AccountView(snap.Data())
	if acc.Disabled {
		e := E.Forbidden("account disabled")
		email := acc.Email
		if email == nil {
			email = dec.Email
		}
		e.AuditActor = &AuditActor{dec.UID, email}
		return nil, e
	}
	return acc, nil
}

// InstallAuth is verifyInstallToken's result.
type InstallAuth struct {
	Token, TokenHash, DeploymentID, SiteSlug string
	TokenDoc                                 *doc.Map
	Mode                                     string // install | upgrade
	Kind                                     string // install | maintenance
}

// BearerInstallToken reads the opi_… token.
func BearerInstallToken(authorization string) (string, error) {
	m := installRe.FindStringSubmatch(authorization)
	if m == nil {
		return "", E.Unauth("missing Authorization: Bearer opi_… (install token)")
	}
	return m[1], nil
}

func currentEnrolledAt(deploymentID, siteSlug string) (*string, error) {
	a, err := Col.Agents(deploymentID).Doc(siteSlug).Get()
	if err != nil {
		return nil, err
	}
	return a.Data().StrPtr("enrolledAt"), nil
}

// CheckMaintenanceState: a maintenance token works only while not revoked and bound to the current enrollment.
func CheckMaintenanceState(t *doc.Map, enrolledAt *string) error {
	site := t.Val("siteSlug")
	if t.Truthy("revokedAt") {
		return E.Gone("this upgrade command was regenerated or revoked", doc.Obj("reason", "revoked", "siteSlug", site))
	}
	bound := t.Str("boundEnrolledAt")
	if enrolledAt == nil || *enrolledAt == "" || bound == "" || bound != *enrolledAt {
		return E.Gone("the enrollment this upgrade command belongs to was revoked", doc.Obj("reason", "enrollment-ended", "siteSlug", site))
	}
	return nil
}

// CheckTokenState is checkTokenState.
func CheckTokenState(t *doc.Map) error {
	site := t.Val("siteSlug")
	if t.Val("kind") == "maintenance" {
		return E.Forbidden("an upgrade (maintenance) command cannot enroll a site — create an install command for a new site", doc.Obj("reason", "maintenance", "siteSlug", site))
	}
	if t.Truthy("revokedAt") {
		return E.Gone("the install token was revoked", doc.Obj("reason", "revoked", "siteSlug", site))
	}
	if t.Truthy("usedAt") {
		return E.Gone("the install token was already used", doc.Obj("reason", "used", "siteSlug", site))
	}
	ms, ok := doc.ParseISO(t.Str("expiresAt"))
	if !ok || ms <= time.Now().UnixMilli() {
		return E.Gone("the install token expired", doc.Obj("reason", "expired", "siteSlug", site))
	}
	return nil
}

// VerifyInstallToken is verifyInstallToken.
func VerifyInstallToken(authorization string) (*InstallAuth, error) {
	token, err := BearerInstallToken(authorization)
	if err != nil {
		return nil, err
	}
	hash := Sha256Hex([]byte(token))
	q, err := DB().CollectionGroup("installTokens").Where("tokenHash", "==", hash).Limit(1).Get()
	if err != nil {
		return nil, err
	}
	if q.Empty() {
		return nil, E.Unauth("unknown install token")
	}
	snap := q.Docs[0]
	t := snap.Data()
	deploymentID := snap.Ref().Parent().Parent().ID()
	kind := "install"
	if t.Val("kind") == "maintenance" {
		kind = "maintenance"
	}
	mode := "install"
	site := t.Str("siteSlug")
	if kind == "maintenance" {
		e, err := currentEnrolledAt(deploymentID, site)
		if err != nil {
			return nil, err
		}
		if err := CheckMaintenanceState(t, e); err != nil {
			return nil, err
		}
		mode = "upgrade"
	} else {
		upgrade := false
		if t.Truthy("usedAt") && !t.Truthy("revokedAt") {
			e, err := currentEnrolledAt(deploymentID, site)
			if err != nil {
				return nil, err
			}
			upgrade = e != nil && *e == t.Str("usedAt")
		}
		if upgrade {
			mode = "upgrade"
		} else if err := CheckTokenState(t); err != nil {
			return nil, err
		}
	}
	dep, err := Col.Deployments().Doc(deploymentID).Get()
	if err != nil {
		return nil, err
	}
	if !dep.Exists() || dep.Data().Truthy("deleted") {
		return nil, E.Gone("the deployment was deleted", doc.Obj("reason", "deleted"))
	}
	return &InstallAuth{token, hash, deploymentID, site, t, mode, kind}, nil
}

// AgentAuth is an authenticated agent request.
type AgentAuth struct {
	DeploymentID, SiteSlug string
	Agent                  *doc.Map
	Dep                    *doc.Map // DecodeDeployment
	// RelayedBy: set when the request arrived through POST /v1/agent/relay
	RelayedBy *string
}

const (
	clockSkewSec = 60
	nonceTTLMs   = 5 * 60 * 1000
)

// VerifyEd25519 verifies a base64url signature with a base64 public key.
func VerifyEd25519(publicKeyB64 string, message []byte, sigB64url string) bool {
	sig := doc.B64Decode(sigB64url)
	raw := doc.B64Decode(publicKeyB64)
	if len(sig) != 64 || len(raw) != 32 {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(raw), message, sig)
}

func agentErr(reason, msg string) error { return E.Unauth(msg, doc.Obj("reason", reason)) }

// SignedParts is what an agent signature covers.
type SignedParts struct {
	Method string
	// Path: path starting at /v1/ including ?query exactly as sent
	Path    string
	Headers map[string]string
	Body    []byte
}

var (
	siteRe  = regexp.MustCompile(`^([A-Za-z0-9]{1,64})/([a-z][a-z0-9-]{1,39})$`)
	tsRe    = regexp.MustCompile(`^\d{1,12}$`)
	nonceRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
)

// VerifyAgent verifies a signed request. Order: site → clock → signature → nonce (a bad signature never burns a nonce).
// allowDeleted: deleted deployments still authenticate (poll answers deleted:true).
func VerifyAgent(p SignedParts, allowDeleted bool) (*AgentAuth, error) {
	site, ts, nonce, sig := p.Headers["x-opm-site"], p.Headers["x-opm-ts"], p.Headers["x-opm-nonce"], p.Headers["x-opm-sig"]
	if site == "" || ts == "" || nonce == "" || sig == "" {
		return nil, agentErr("signature", "missing X-OPM-Site / X-OPM-Ts / X-OPM-Nonce / X-OPM-Sig")
	}
	m := siteRe.FindStringSubmatch(site)
	if m == nil {
		return nil, agentErr("unknown-site", "X-OPM-Site must be <deploymentId>/<siteSlug>")
	}
	deploymentID, siteSlug := m[1], m[2]
	s, err := DB().GetAll(Col.Deployments().Doc(deploymentID), Col.Agents(deploymentID).Doc(siteSlug))
	if err != nil {
		return nil, err
	}
	if !s[0].Exists() {
		return nil, agentErr("unknown-site", "unknown deployment")
	}
	dep := DecodeDeployment(s[0].Data())
	if dep.Truthy("deleted") && !allowDeleted {
		return nil, agentErr("unknown-site", "the deployment was deleted")
	}
	if !s[1].Exists() {
		return nil, agentErr("unknown-site", "site "+siteSlug+" is not enrolled")
	}
	agent := s[1].Data()
	if !tsRe.MatchString(ts) {
		return nil, agentErr("clock", "X-OPM-Ts must be unix seconds")
	}
	tn, _ := strconv.ParseInt(ts, 10, 64)
	skew := time.Now().Unix() - tn
	if skew < 0 {
		skew = -skew
	}
	if skew > clockSkewSec {
		return nil, agentErr("clock", "clock skew "+strconv.FormatInt(skew, 10)+" s > "+itoa(clockSkewSec)+" s (use X-OPM-Server-Time)")
	}
	if !nonceRe.MatchString(nonce) {
		return nil, agentErr("nonce", "X-OPM-Nonce must be base64url")
	}
	str := strings.ToUpper(p.Method) + "\n" + p.Path + "\n" + ts + "\n" + nonce + "\n" + Sha256Hex(p.Body)
	if !VerifyEd25519(agent.Str("publicKey"), []byte(str), sig) {
		return nil, agentErr("signature", "signature does not verify with the enrolled site key")
	}
	nonceID := Sha256Hex([]byte(deploymentID + "/" + siteSlug + "/" + nonce))
	exp := func() doc.Timestamp { return doc.TimestampFromMillis(time.Now().UnixMilli() + nonceTTLMs) }
	if err := Col.AgentNonces().Doc(nonceID).Create(doc.Obj("expiresAt", exp())); err != nil {
		if !driver.IsAlreadyExists(err) {
			return nil, err
		}
		// an expired entry that TTL has not removed yet does not count
		old, err := Col.AgentNonces().Doc(nonceID).Get()
		if err != nil {
			return nil, err
		}
		if t, ok := old.Data().Val("expiresAt").(doc.Timestamp); ok && t.Millis() > time.Now().UnixMilli() {
			return nil, agentErr("nonce", "nonce already used (replay)")
		}
		if err := Col.AgentNonces().Doc(nonceID).Set(doc.Obj("expiresAt", exp())); err != nil {
			return nil, err
		}
	}
	return &AgentAuth{deploymentID, siteSlug, agent, dep, nil}, nil
}
