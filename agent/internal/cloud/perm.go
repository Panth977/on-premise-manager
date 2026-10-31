package cloud

import (
	"opm/agent/internal/cloud/doc"
)

// TargetOf: the permission target of a deployment document.
func TargetOf(d *doc.Map) *AccessTarget {
	return &AccessTarget{DeploymentID: d.Str("deploymentId"), TagSlugs: d.Strs("tagSlugs"), ManagerAccountUIDs: d.Strs("managerAccountUids")}
}

// Need fails with 403 unless the caller has the permission.
func Need(c *Ctx, resource, action string, t *AccessTarget) error {
	if !Allowed(c.Account, resource, action, t) {
		on := ""
		if t != nil {
			on = " on deployment " + t.DeploymentID
		}
		return E.Forbidden("needs " + resource + "." + action + on)
	}
	return nil
}

// Has is the boolean form of Need.
func Has(c *Ctx, resource, action string, t *AccessTarget) bool {
	return Allowed(c.Account, resource, action, t)
}

// LoadDep loads a deployment for a human endpoint ("" = the deploymentId path parameter): deleted or invisible → 404.
func LoadDep(c *Ctx, id string) (*doc.Map, error) {
	if id == "" {
		id = c.Params["deploymentId"]
	}
	snap, err := Col.Deployments().Doc(id).Get()
	if err != nil {
		return nil, err
	}
	if !snap.Exists() {
		return nil, E.NotFound("deployment not found")
	}
	d := DecodeDeployment(snap.Data())
	if d.Truthy("deleted") || !CanSeeDeployment(c.Account, TargetOf(d)) {
		return nil, E.NotFound("deployment not found")
	}
	return d, nil
}

// NeedDep is Need on a loaded deployment.
func NeedDep(c *Ctx, d *doc.Map, resource, action string) error {
	return Need(c, resource, action, TargetOf(d))
}

// HasOnAnyDeployment: a rule with that resource + action, or a manager of some deployment.
func HasOnAnyDeployment(a *Account, resource, action string) (bool, error) {
	if a.Disabled {
		return false, nil
	}
	for _, r := range a.Access {
		if r.Resource == resource && has(r.Actions, action) {
			return true, nil
		}
	}
	q, err := Col.Deployments().Where("managerAccountUids", "array-contains", a.UID).Limit(5).Get()
	if err != nil {
		return false, err
	}
	for _, d := range q.Docs {
		if !d.Data().Truthy("deleted") {
			return true, nil
		}
	}
	return false, nil
}
