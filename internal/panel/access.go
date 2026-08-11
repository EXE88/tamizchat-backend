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
		p.printf("  %s\n\n", bold("رول‌ها و دسترسی‌ها"))

		p.println("  1) فهرست رول‌ها")
		p.println("  2) ساخت رول")
		p.println("  3) ویرایش مجوزهای یک رول")
		p.println("  4) حذف رول")
		p.println("  5) دادن رول به یک کاربر")
		p.println("  6) گرفتن رول از یک کاربر")
		p.println("  0) بازگشت\n")

		switch p.ask("انتخاب کنید") {
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
			p.warn("گزینهٔ نامعتبر")
		}
	}
}

func (p *Panel) listRoles() {
	p.clear()
	p.banner()
	p.printf("  %s\n\n", bold("رول‌ها"))

	roles := p.access.AllRoles()
	p.printf("  %-4s %-20s %-8s %-10s %s\n", "#", "نام", "رتبه", "پیش‌فرض", "شناسه")
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
	name := p.ask("نام رول")
	if name == "" {
		p.warn("لغو شد")
		return
	}

	priority := 0
	if raw := p.ask("رتبه (عدد بزرگ‌تر = قوی‌تر، پیش‌فرض ۰)"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			p.warn("رتبه باید عدد باشد")
			return
		}
		priority = n
	}

	perms := p.askPermissions(0)
	spec := access.RoleSpec{Name: &name, Priority: &priority, Permissions: &perms}
	if _, err := p.access.CreateRole(ctx, spec); err != nil {
		p.warn("خطا: " + err.Error())
		return
	}
	p.ok("رول ساخته شد — روی سرورِ در حال اجرا پس از ری‌استارت اعمال می‌شود")
}

func (p *Panel) editRolePermissions(ctx context.Context) {
	role, ok := p.pickRole("شمارهٔ رول برای ویرایش")
	if !ok {
		return
	}

	perms := p.askPermissions(authz.Permission(role.Permissions))
	spec := access.RoleSpec{Permissions: &perms}
	if _, err := p.access.UpdateRole(ctx, role.ID, spec); err != nil {
		p.warn("خطا: " + err.Error())
		return
	}
	p.ok("مجوزها ذخیره شد")
}

func (p *Panel) deleteRole(ctx context.Context) {
	role, ok := p.pickRole("شمارهٔ رول برای حذف")
	if !ok {
		return
	}
	if p.ask("حذف رول «"+role.Name+"»؟ (y/n)") != "y" {
		p.warn("لغو شد")
		return
	}
	if err := p.access.DeleteRole(ctx, role.ID); err != nil {
		p.warn("خطا: " + err.Error())
		return
	}
	p.ok("رول حذف شد")
}

// assignRole grants or revokes a role. The panel is not subject to the rank
// checks the protocol enforces: whoever runs it already owns the server.
func (p *Panel) assignRole(ctx context.Context, grant bool) {
	role, ok := p.pickRole("شمارهٔ رول")
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
		p.warn("خطا: " + err.Error())
		return
	}
	p.ok("انجام شد — کاربر باید دوباره وصل شود تا تغییر را ببیند")
}

// pickUser lets the operator choose from the known users, or paste a UUID for
// somebody who has not connected yet.
func (p *Panel) pickUser(ctx context.Context) (string, bool) {
	users, err := p.store.RecentUsers(ctx, 30)
	if err != nil {
		p.warn("خواندن کاربران ناموفق بود: " + err.Error())
		return "", false
	}

	p.println("")
	for i, u := range users {
		p.printf("  %-4d %-24s %s\n", i+1, truncate(u.Username, 24), u.ClientUUID)
	}
	p.println("")

	raw := p.ask("شمارهٔ کاربر یا شناسهٔ کلاینت")
	if raw == "" {
		return "", false
	}
	if n, err := strconv.Atoi(raw); err == nil {
		if n < 1 || n > len(users) {
			p.warn("شمارهٔ نامعتبر")
			return "", false
		}
		return users[n-1].ClientUUID, true
	}
	return strings.ToLower(raw), true
}

func (p *Panel) pickRole(prompt string) (storage.Role, bool) {
	roles := p.access.AllRoles()
	if len(roles) == 0 {
		p.warn("رولی وجود ندارد")
		return storage.Role{}, false
	}

	p.println("")
	for i, r := range roles {
		p.printf("  %-4d %-20s رتبه %d\n", i+1, truncate(r.Name, 20), r.Priority)
	}
	p.println("")

	n, err := strconv.Atoi(p.ask(prompt))
	if err != nil || n < 1 || n > len(roles) {
		p.warn("شمارهٔ نامعتبر")
		return storage.Role{}, false
	}
	return roles[n-1], true
}

// askPermissions walks the permission list and asks about each one, starting
// from the current mask.
func (p *Panel) askPermissions(current authz.Permission) authz.Permission {
	p.printf("\n  برای هر مجوز y یا n بزنید (خالی = بدون تغییر)\n\n")

	mask := current
	for _, d := range authz.All {
		has := mask&d.Perm != 0
		answer := p.ask(d.Title + " [" + yesNoShort(has) + "]")
		switch strings.ToLower(answer) {
		case "y", "yes", "بله":
			mask |= d.Perm
		case "n", "no", "خیر":
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
		p.printf("  %s\n\n", bold("بن‌ها و میوت‌ها"))

		list, err := p.store.ListSanctions(ctx)
		if err != nil {
			p.warn("خواندن ناموفق بود: " + err.Error())
			return
		}

		if len(list) == 0 {
			p.println("  هیچ محدودیتی ثبت نشده است.\n")
		} else {
			p.printf("  %-4s %-6s %-20s %-38s %s\n", "#", "نوع", "کاربر", "شناسه", "تا")
			for i, s := range list {
				p.printf("  %-4d %-6s %-20s %-38s %s\n", i+1, sanctionKind(s.Kind),
					truncate(s.Username, 20), s.ClientUUID, expiryLabel(s.ExpiresAt))
				if s.Reason != "" {
					p.printf("       %s\n", dim("دلیل: "+s.Reason))
				}
			}
			p.println("")
		}

		p.println("  l) برداشتن یک محدودیت")
		p.println("  0) بازگشت\n")

		switch p.ask("انتخاب کنید") {
		case "l":
			p.liftSanction(ctx, list)
		case "0", "":
			return
		default:
			p.warn("گزینهٔ نامعتبر")
		}
	}
}

func (p *Panel) liftSanction(ctx context.Context, list []storage.Sanction) {
	if len(list) == 0 {
		p.warn("چیزی برای برداشتن نیست")
		return
	}
	p.println("")
	n, err := strconv.Atoi(p.ask("شمارهٔ ردیف"))
	if err != nil || n < 1 || n > len(list) {
		p.warn("شمارهٔ نامعتبر")
		return
	}

	target := list[n-1]
	if _, err := p.access.Lift(ctx, target.Kind, target.ClientUUID); err != nil {
		p.warn("خطا: " + err.Error())
		return
	}
	p.ok("برداشته شد")
}

// showModLog displays what moderators have been doing.
func (p *Panel) showModLog(ctx context.Context) {
	p.clear()
	p.banner()
	p.printf("  %s\n\n", bold("لاگ اقدامات مدیریتی"))

	entries, err := p.store.RecentModLog(ctx, 40)
	if err != nil {
		p.warn("خواندن لاگ ناموفق بود: " + err.Error())
		return
	}
	if len(entries) == 0 {
		p.println("  هنوز اقدامی ثبت نشده است.\n")
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
		return "بدون مجوز"
	}
	if len(keys) == len(authz.All) {
		return "تمام مجوزها"
	}
	return strings.Join(keys, ", ")
}

func sanctionKind(kind string) string {
	if kind == storage.SanctionMute {
		return "میوت"
	}
	return "بن"
}

func expiryLabel(expiresAt int64) string {
	if expiresAt == 0 {
		return "دائمی"
	}
	return time.Unix(expiresAt, 0).Format("2006-01-02 15:04")
}

func yesNoShort(b bool) string {
	if b {
		return "بله"
	}
	return "خیر"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
