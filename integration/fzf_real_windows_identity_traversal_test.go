//go:build windows

package integration

import "sort"

// traverseWindowsProcessIdentities returns the identities reachable from a
// verified root. A child is accepted only when its creation marker is newer
// than the marker of the identity that owns its PPID.
func traverseWindowsProcessIdentities(root windowsProcessIdentityKey, nodes map[uint32]windowsProcessNode, observed map[windowsProcessIdentityKey]struct{}) (map[windowsProcessIdentityKey]struct{}, bool) {
	accepted := make(map[windowsProcessIdentityKey]struct{}, len(observed))
	for key := range observed {
		accepted[key] = struct{}{}
	}

	parents := make([]windowsProcessIdentityKey, 0, len(accepted)+1)
	live := false
	rootNode, rootPresent := nodes[root.pid]
	if root.marker != 0 {
		switch {
		case rootPresent && rootNode.queryErr != nil:
			return accepted, true
		case rootPresent && rootNode.creationMarker == root.marker:
			parents = append(parents, root)
			live = true
		}
	}

	for key := range accepted {
		node, present := nodes[key.pid]
		switch {
		case !present:
			// An accepted parent may have exited while its accepted child is
			// still reported with the exited PID as its PPID. Keep the known
			// identity as a parent unless that PID has already been reused.
			if key.marker != 0 {
				parents = append(parents, key)
			}
		case node.queryErr != nil:
			live = true
		case node.creationMarker == key.marker && key.marker != 0:
			parents = append(parents, key)
			live = true
		}
	}

	for index := 0; index < len(parents); index++ {
		parent := parents[index]
		for pid, node := range nodes {
			if node.queryErr != nil || node.ppid != parent.pid || node.creationMarker <= parent.marker {
				continue
			}
			child := windowsProcessIdentityKey{pid: pid, marker: node.creationMarker}
			if hasWindowsProcessIdentityPID(accepted, pid) {
				continue
			}
			accepted[child] = struct{}{}
			parents = append(parents, child)
			live = true
		}
	}
	return accepted, live
}

func hasWindowsProcessIdentityPID(identities map[windowsProcessIdentityKey]struct{}, pid uint32) bool {
	for identity := range identities {
		if identity.pid == pid {
			return true
		}
	}
	return false
}

func sortedWindowsProcessIdentities(identities map[windowsProcessIdentityKey]struct{}) []windowsProcessIdentityKey {
	result := make([]windowsProcessIdentityKey, 0, len(identities))
	for identity := range identities {
		result = append(result, identity)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].pid != result[right].pid {
			return result[left].pid < result[right].pid
		}
		return result[left].marker < result[right].marker
	})
	return result
}
