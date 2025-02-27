package ovs

import (
	"context"
	"errors"
	"fmt"

	"github.com/ovn-org/libovsdb/client"
	"github.com/ovn-org/libovsdb/model"
	"github.com/ovn-org/libovsdb/ovsdb"
	"github.com/scylladb/go-set/strset"

	ovsclient "github.com/kubeovn/kube-ovn/pkg/ovsdb/client"
	"github.com/kubeovn/kube-ovn/pkg/ovsdb/ovnnb"
	"github.com/kubeovn/kube-ovn/pkg/util"
)

// AddLogicalRouterPolicy add a policy route to logical router
func (c *ovnClient) AddLogicalRouterPolicy(lrName string, priority int, match, action string, nextHops []string, externalIDs map[string]string) error {
	fnFilter := func(policy *ovnnb.LogicalRouterPolicy) bool {
		return policy.Priority == priority && policy.Match == match
	}
	policyList, err := c.listLogicalRouterPoliciesByFilter(lrName, fnFilter)
	if err != nil {
		return fmt.Errorf("get policy priority %d match %s in logical router %s: %v", priority, match, lrName, err)
	}

	var found bool
	duplicate := make([]string, 0, len(policyList))
	for _, policy := range policyList {
		if found || policy.Action != action || (policy.Action == ovnnb.LogicalRouterPolicyActionReroute && !strset.New(nextHops...).IsEqual(strset.New(policy.Nexthops...))) {
			duplicate = append(duplicate, policy.UUID)
		} else {
			found = true
		}
	}
	for _, uuid := range duplicate {
		if err = c.DeleteLogicalRouterPolicyByUUID(lrName, uuid); err != nil {
			return err
		}
	}
	if len(duplicate) == len(policyList) {
		policy := c.newLogicalRouterPolicy(priority, match, action, nextHops, externalIDs)
		if err := c.CreateLogicalRouterPolicies(lrName, policy); err != nil {
			return fmt.Errorf("add policy to logical router %s: %v", lrName, err)
		}
	}

	return nil
}

// CreateLogicalRouterPolicies create several logical router policy once
func (c *ovnClient) CreateLogicalRouterPolicies(lrName string, policies ...*ovnnb.LogicalRouterPolicy) error {
	if len(policies) == 0 {
		return nil
	}

	models := make([]model.Model, 0, len(policies))
	policyUUIDs := make([]string, 0, len(policies))
	for _, policy := range policies {
		if policy != nil {
			models = append(models, model.Model(policy))
			policyUUIDs = append(policyUUIDs, policy.UUID)
		}
	}

	createPoliciesOp, err := c.ovnNbClient.Create(models...)
	if err != nil {
		return fmt.Errorf("generate operations for creating policies: %v", err)
	}

	policyAddOp, err := c.LogicalRouterUpdatePolicyOp(lrName, policyUUIDs, ovsdb.MutateOperationInsert)
	if err != nil {
		return fmt.Errorf("generate operations for adding policies to logical router %s: %v", lrName, err)
	}

	ops := make([]ovsdb.Operation, 0, len(createPoliciesOp)+len(policyAddOp))
	ops = append(ops, createPoliciesOp...)
	ops = append(ops, policyAddOp...)

	if err = c.Transact("lr-policies-add", ops); err != nil {
		return fmt.Errorf("add policies to %s: %v", lrName, err)
	}

	return nil
}

// DeleteLogicalRouterPolicy delete policy from logical router
func (c *ovnClient) DeleteLogicalRouterPolicy(lrName string, priority int, match string) error {
	policyList, err := c.GetLogicalRouterPolicy(lrName, priority, match, true)
	if err != nil {
		return err
	}

	for _, p := range policyList {
		if err := c.DeleteLogicalRouterPolicyByUUID(lrName, p.UUID); err != nil {
			return err
		}
	}

	return nil
}

// DeleteLogicalRouterPolicy delete some policies from logical router once
func (c *ovnClient) DeleteLogicalRouterPolicies(lrName string, priority int, externalIDs map[string]string) error {
	// remove policies from logical router
	policies, err := c.ListLogicalRouterPolicies(lrName, priority, externalIDs)
	if err != nil {
		return err
	}
	if len(policies) == 0 {
		return nil
	}

	policiesUUIDs := make([]string, 0, len(policies))
	for _, policy := range policies {
		policiesUUIDs = append(policiesUUIDs, policy.UUID)
	}

	ops, err := c.LogicalRouterUpdatePolicyOp(lrName, policiesUUIDs, ovsdb.MutateOperationDelete)
	if err != nil {
		return fmt.Errorf("generate operations for removing policy %v from logical router %s: %v", policiesUUIDs, lrName, err)
	}
	if err = c.Transact("lr-policies-del", ops); err != nil {
		return fmt.Errorf("delete logical router policy %v from logical router %s: %v", policiesUUIDs, lrName, err)
	}
	return nil
}

func (c *ovnClient) DeleteLogicalRouterPolicyByUUID(lrName, uuid string) error {
	// remove policy from logical router
	ops, err := c.LogicalRouterUpdatePolicyOp(lrName, []string{uuid}, ovsdb.MutateOperationDelete)
	if err != nil {
		return fmt.Errorf("generate operations for removing policy '%s' from logical router %s: %v", uuid, lrName, err)
	}
	if err = c.Transact("lr-policy-del", ops); err != nil {
		return fmt.Errorf("delete logical router policy '%s' from logical router %s: %v", uuid, lrName, err)
	}
	return nil
}

func (c *ovnClient) DeleteLogicalRouterPolicyByNexthop(lrName string, priority int, nexthop string) error {
	policyList, err := c.listLogicalRouterPoliciesByFilter(lrName, func(route *ovnnb.LogicalRouterPolicy) bool {
		if route.Priority != priority {
			return false
		}
		return (route.Nexthop != nil && *route.Nexthop == nexthop) || util.ContainsString(route.Nexthops, nexthop)
	})
	if err != nil {
		return err
	}
	for _, policy := range policyList {
		if err = c.DeleteLogicalRouterPolicyByUUID(lrName, policy.UUID); err != nil {
			return err
		}
	}
	return nil
}

// ClearLogicalRouterPolicy clear policy from logical router once
func (c *ovnClient) ClearLogicalRouterPolicy(lrName string) error {
	lr, err := c.GetLogicalRouter(lrName, false)
	if err != nil {
		return fmt.Errorf("get logical router %s: %v", lrName, err)
	}

	// clear logical router policy
	lr.Policies = nil
	ops, err := c.UpdateLogicalRouterOp(lr, &lr.Policies)
	if err != nil {
		return fmt.Errorf("generate operations for clear logical router %s policy: %v", lrName, err)
	}
	if err = c.Transact("lr-policy-clear", ops); err != nil {
		return fmt.Errorf("clear logical router %s policy: %v", lrName, err)
	}

	return nil
}

// GetLogicalRouterPolicy get logical router policy by priority and match,
// be consistent with ovn-nbctl which priority and match determine one policy in logical router
func (c *ovnClient) GetLogicalRouterPolicy(lrName string, priority int, match string, ignoreNotFound bool) ([]*ovnnb.LogicalRouterPolicy, error) {
	// this is necessary because may exist same priority and match policy in different logical router
	if len(lrName) == 0 {
		return nil, fmt.Errorf("the logical router name is required")
	}

	fnFilter := func(policy *ovnnb.LogicalRouterPolicy) bool {
		return policy.Priority == priority && policy.Match == match
	}
	policyList, err := c.listLogicalRouterPoliciesByFilter(lrName, fnFilter)
	if err != nil {
		return nil, fmt.Errorf("get policy priority %d match %s in logical router %s: %v", priority, match, lrName, err)
	}

	// not found
	if len(policyList) == 0 {
		if ignoreNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("not found policy priority %d match %s in logical router %s", priority, match, lrName)
	}

	return policyList, nil
}

// GetLogicalRouterPolicyByUUID get logical router policy by UUID
func (c *ovnClient) GetLogicalRouterPolicyByUUID(uuid string) (*ovnnb.LogicalRouterPolicy, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	policy := &ovnnb.LogicalRouterPolicy{UUID: uuid}
	if err := c.Get(ctx, policy); err != nil {
		return nil, err
	}

	return policy, nil
}

func (c *ovnClient) GetLogicalRouterPoliciesByExtID(lrName, key, value string) ([]*ovnnb.LogicalRouterPolicy, error) {
	fnFilter := func(policy *ovnnb.LogicalRouterPolicy) bool {
		return len(policy.ExternalIDs) != 0 && policy.ExternalIDs[key] == value
	}
	return c.listLogicalRouterPoliciesByFilter(lrName, fnFilter)
}

// ListLogicalRouterPolicies list route policy which match the given externalIDs
func (c *ovnClient) ListLogicalRouterPolicies(lrName string, priority int, externalIDs map[string]string) ([]*ovnnb.LogicalRouterPolicy, error) {
	return c.listLogicalRouterPoliciesByFilter(lrName, policyFilter(priority, externalIDs))
}

// newLogicalRouterPolicy return logical router policy with basic information
func (c *ovnClient) newLogicalRouterPolicy(priority int, match, action string, nextHops []string, externalIDs map[string]string) *ovnnb.LogicalRouterPolicy {
	return &ovnnb.LogicalRouterPolicy{
		UUID:        ovsclient.NamedUUID(),
		Priority:    priority,
		Match:       match,
		Action:      action,
		Nexthops:    nextHops,
		ExternalIDs: externalIDs,
	}
}

// policyFilter filter policies which match the given externalIDs
func policyFilter(priority int, externalIDs map[string]string) func(policy *ovnnb.LogicalRouterPolicy) bool {
	return func(policy *ovnnb.LogicalRouterPolicy) bool {
		if len(policy.ExternalIDs) < len(externalIDs) {
			return false
		}

		if len(policy.ExternalIDs) != 0 {
			for k, v := range externalIDs {
				// if only key exist but not value in externalIDs, we should include this lsp,
				// it's equal to shell command `ovn-nbctl --columns=xx find logical_router_policy external_ids:key!=\"\"`
				if len(v) == 0 {
					if len(policy.ExternalIDs[k]) == 0 {
						return false
					}
				} else {
					if policy.ExternalIDs[k] != v {
						return false
					}
				}
			}
		}

		if priority >= 0 && priority != policy.Priority {
			return false
		}

		return true
	}
}

func (c *ovnClient) DeleteRouterPolicy(lr *ovnnb.LogicalRouter, uuid string) error {
	ops, err := c.ovnNbClient.Where(lr).Mutate(lr, model.Mutation{
		Field:   &lr.Policies,
		Mutator: ovsdb.MutateOperationDelete,
		Value:   []string{uuid},
	})
	if err != nil {
		return fmt.Errorf("failed to generate delete operations for router %s: %v", uuid, err)
	}
	if err = c.Transact("lr-policy-delete", ops); err != nil {
		return fmt.Errorf("failed to delete route policy %s: %v", uuid, err)
	}
	return nil
}

func (c *ovnClient) listLogicalRouterPoliciesByFilter(lrName string, filter func(route *ovnnb.LogicalRouterPolicy) bool) ([]*ovnnb.LogicalRouterPolicy, error) {
	lr, err := c.GetLogicalRouter(lrName, false)
	if err != nil {
		return nil, err
	}

	policyList := make([]*ovnnb.LogicalRouterPolicy, 0, len(lr.Policies))
	for _, uuid := range lr.Policies {
		policy, err := c.GetLogicalRouterPolicyByUUID(uuid)
		if err != nil {
			if errors.Is(err, client.ErrNotFound) {
				continue
			}
			return nil, err
		}
		if filter == nil || filter(policy) {
			policyList = append(policyList, policy)
		}
	}

	return policyList, nil
}
