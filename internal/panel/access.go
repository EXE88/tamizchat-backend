package panel

import (
	"context"
	"strconv"
	"strings"
	"time"

	"tamizchat/internal/access"
	"tamizchat/internal/authz"
	"tamizchat/internal/storage"
)

// accessMenu is where the operator hands out the first admin role. Everything
// else can be done from the client once someone has it.
func (p *Panel) accessMenu(ctx context.Context) {
	for {
		p.clear()
		p.banner()
		p.printf("  %s\n\n", bold("Roles and permissions"))

		p.println("  1) List roles")
		p.println("  2) Create a role")
		p.println("  3) Edit a role's permissions")
		p.println("  4) Delete a role")
		p.println("  5) Grant a role to a user")
		p.println("  6) Revoke a role from a user")
		p.println("  0) Back\n")

		switch p.ask("Choose") {
		case "1":
			p.listRoles()
		case "2":
			p.createRole(ctx)
		case "3":
			p.editRolePermissions(ctx)
		case "4":
			p.deleteRole(ctx)
		case "5":
			p.assignRole(ctx, true)
		case "6":
			p.assignRole(ctx, false)
		case "0", "":
			return
		default:
			p.warn("Invalid choice")
		}
	}
}

func (p *Panel) listRoles() {
	p.clear()
	p.banner()
	p.printf("  %s\n\n", bold("Roles"))

	roles := p.access.AllRoles()
	p.printf("  %-4s %-20s %-8s %-10s %s\n", "#", "NAME", "PRIORITY", "DEFAULT", "ID")
	for i, r := range roles {
		p.printf("  %-4d %-20s %-8d %-10s %s\n", i+1, truncate(r.Name, 20), r.Priority,
			yesNoShort(r.IsDefault), r.ID)
		p.printf("       %s\n", dim(permissionSummary(r.Permissions)))
	}
	p.println("")
	p.pause()
}

func (p *Panel) createRole(ctx context.Context) {
	p.println("")
	name := p.ask("Role name")
	if name == "" {
		p.warn("Cancelled")
		return
	}

	priority := 0
	if raw := p.ask("Priority (higher = stronger, default 0)"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			p.warn("Priority must be a number")
			return
		}
		priority = n
	}

	perms := p.askPermissions(0)
	spec := access.RoleSpec{Name: &name, Priority: &priority, Permissions: &perms}
	if _, err := p.access.CreateRole(ctx, spec); err != nil {
		p.warn("Error: " + err.Error())
		return
	}
	p.ok("Role created — a running server picks it up after a restart")
}

func (p *Panel) editRolePermissions(ctx context.Context) {
	role, ok := p.pickRole("Number of the role to edit")
	if !ok {
		return
	}

	perms := p.askPermissions(authz.Permission(role.Permissions))
	spec := access.RoleSpec{Permissions: &perms}
	if _, err := p.access.UpdateRole(ctx, role.ID, spec); err != nil {
		p.warn("Error: " + err.Error())
		return
	}
	p.ok("Permissions saved")
}

func (p *Panel) deleteRole(ctx context.Context) {
	role, ok := p.pickRole("Number of the role to delete")
	if !ok {
		return
	}
	if p.ask("Delete role \""+role.Name+"\"? (y/n)") != "y" {
		p.warn("Cancelled")
		return
	}
	if err := p.access.DeleteRole(ctx, role.ID); err != nil {
		p.warn("Error: " + err.Error())
		return
	}
	p.ok("Role deleted")
}

// assignRole grants or revokes a role. The panel is not subject to the rank
// checks the protocol enforces: whoever runs it already owns the server.
func (p *Panel) assignRole(ctx context.Context, grant bool) {
	role, ok := p.pickRole("Role number")
	if !ok {
		return
	}

	uuid, ok := p.pickUser(ctx)
	if !ok {
		return
	}

	var err error
	if grant {
		err = p.access.Grant(ctx, "", uuid, role.ID)
	} else {
		err = p.access.Revoke(ctx, "", uuid, role.ID)
	}
	if err != nil {
		p.warn("Error: " + err.Error())
		return
	}
	p.ok("Done — the user must reconnect to see the change")
}

// pickUser lets the operator choose from the known users, or paste a UUID for
// somebody who has not connected yet.
func (p *Panel) pickUser(ctx context.Context) (string, bool) {
	users, err := p.store.RecentUsers(ctx, 30)
	if err != nil {
		p.warn("Could not read users: " + err.Error())
		return "", false
	}

	p.println("")
	for i, u := range users {
		p.printf("  %-4d %-24s %s\n", i+1, truncate(u.Username, 24), u.ClientUUID)
	}
	p.println("")

	raw := p.ask("User number or client ID")
	if raw == "" {
		return "", false
	}
	if n, err := strconv.Atoi(raw); err == nil {
		if n < 1 || n > len(users) {
			p.warn("Invalid number")
			return "", false
		}
		return users[n-1].ClientUUID, true
	}
	return strings.ToLower(raw), true
}

func (p *Panel) pickRole(prompt string) (storage.Role, bool) {
	roles := p.access.AllRoles()
	if len(roles) == 0 {
		p.warn("There are no roles")
		return storage.Role{}, false
	}

	p.println("")
	for i, r := range roles {
		p.printf("  %-4d %-20s priority %d\n", i+1, truncate(r.Name, 20), r.Priority)
	}
	p.println("")

	n, err := strconv.Atoi(p.ask(prompt))
	if err != nil || n < 1 || n > len(roles) {
		p.warn("Invalid number")
		return storage.Role{}, false
	}
	return roles[n-1], true
}

// askPermissions walks the permission list and asks about each one, starting
// from the current mask.
func (p *Panel) askPermissions(current authz.Permission) authz.Permission {
	p.printf("\n  Answer y or n for each permission (empty = leave unchanged)\n\n")

	mask := current
	for _, d := range authz.All {
		has := mask&d.Perm != 0
		answer := p.ask(d.Title + " [" + yesNoShort(has) + "]")
		switch strings.ToLower(answer) {
		case "y", "yes":
			mask |= d.Perm
		case "n", "no":
			mask &^= d.Perm
		}
	}
	return mask
}

// sanctionsMenu lists and lifts bans and mutes.
func (p *Panel) sanctionsMenu(ctx context.Context) {
	for {
		p.clear()
		p.banner()
		p.printf("  %s\n\n", bold("Bans and mutes"))

		list, err := p.store.ListSanctions(ctx)
		if err != nil {
			p.warn("Read failed: " + err.Error())
			return
		}

		if len(list) == 0 {
			p.println("  No sanctions on record.\n")
		} else {
			p.printf("  %-4s %-6s %-20s %-38s %s\n", "#", "KIND", "USER", "ID", "UNTIL")
			for i, s := range list {
				p.printf("  %-4d %-6s %-20s %-38s %s\n", i+1, sanctionKind(s.Kind),
					truncate(s.Username, 20), s.ClientUUID, expiryLabel(s.ExpiresAt))
				if s.Reason != "" {
					p.printf("       %s\n", dim("reason: "+s.Reason))
				}
			}
			p.println("")
		}

		p.println("  l) Lift a sanction")
		p.println("  0) Back\n")

		switch p.ask("Choose") {
		case "l":
			p.liftSanction(ctx, list)
		case "0", "":
			return
		default:
			p.warn("Invalid choice")
		}
	}
}

func (p *Panel) liftSanction(ctx context.Context, list []storage.Sanction) {
	if len(list) == 0 {
		p.warn("Nothing to lift")
		return
	}
	p.println("")
	n, err := strconv.Atoi(p.ask("Row number"))
	if err != nil || n < 1 || n > len(list) {
		p.warn("Invalid number")
		return
	}

	target := list[n-1]
	if _, err := p.access.Lift(ctx, target.Kind, target.ClientUUID); err != nil {
		p.warn("Error: " + err.Error())
		return
	}
	p.ok("Lifted")
}

// showModLog displays what moderators have been doing.
func (p *Panel) showModLog(ctx context.Context) {
	p.clear()
	p.banner()
	p.printf("  %s\n\n", bold("Moderation log"))

	entries, err := p.store.RecentModLog(ctx, 40)
	if err != nil {
		p.warn("Could not read the log: " + err.Error())
		return
	}
	if len(entries) == 0 {
		p.println("  No actions recorded yet.\n")
		p.pause()
		return
	}

	for _, e := range entries {
		p.printf("  %s  %-12s %s → %s  %s\n",
			time.Unix(e.At, 0).Format("01-02 15:04"),
			e.Action,
			truncate(orDash(e.ActorName), 16),
			truncate(orDash(e.TargetName), 16),
			dim(e.Detail))
	}
	p.println("")
	p.pause()
}

func permissionSummary(mask uint64) string {
	keys := authz.Keys(authz.Permission(mask))
	if len(keys) == 0 {
		return "no permissions"
	}
	if len(keys) == len(authz.All) {
		return "all permissions"
	}
	return strings.Join(keys, ", ")
}

func sanctionKind(kind string) string {
	if kind == storage.SanctionMute {
		return "mute"
	}
	return "ban"
}

func expiryLabel(expiresAt int64) string {
	if expiresAt == 0 {
		return "permanent"
	}
	return time.Unix(expiresAt, 0).Format("2006-01-02 15:04")
}

func yesNoShort(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
