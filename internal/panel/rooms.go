package panel

import (
	"context"
	"strconv"
	"time"

	"tamizchat/internal/config"
	"tamizchat/internal/rooms"
	"tamizchat/internal/storage"
	"tamizchat/internal/textutil"
)

// roomsMenu manages room definitions straight in the database. Members are not
// shown here: who is inside a room lives in the running server's memory, which
// the panel cannot see until the control socket of phase 10.
func (p *Panel) roomsMenu(ctx context.Context) {
	for {
		list, err := p.store.ListRooms(ctx)
		if err != nil {
			p.warn("خواندن روم‌ها ناموفق بود: " + err.Error())
			return
		}

		p.clear()
		p.banner()
		p.printf("  %s (%d روم)\n\n", bold("مدیریت روم‌ها"), len(list))

		if len(list) == 0 {
			p.println("  هنوز رومی ساخته نشده است.\n")
		} else {
			p.printf("  %-4s %-28s %-10s %-8s %s\n", "#", "نام", "رمز", "ظرفیت", "ساخته‌شده")
			for i, r := range list {
				p.printf("  %-4d %-28s %-10s %-8d %s\n", i+1, truncate(r.Name, 28),
					hasPassword(r.Password), r.Capacity,
					time.Unix(r.CreatedAt, 0).Format("2006-01-02"))
			}
			p.println("")
		}

		p.println("  n) ساخت روم جدید")
		p.println("  e) ویرایش روم")
		p.println("  d) حذف روم")
		p.println("  0) بازگشت\n")

		switch p.ask("انتخاب کنید") {
		case "n":
			p.createRoom(ctx)
		case "e":
			p.editRoom(ctx, list)
		case "d":
			p.deleteRoom(ctx, list)
		case "0", "":
			return
		default:
			p.warn("گزینهٔ نامعتبر")
		}
	}
}

func (p *Panel) createRoom(ctx context.Context) {
	p.println("")
	name, ok := p.askRoomName("نام روم")
	if !ok {
		return
	}

	password := p.ask("رمز روم (خالی = بدون رمز)")

	capacity := p.cfg.Int(config.KeyRoomsDefaultMaxUsers)
	if raw := p.ask("ظرفیت (خالی = پیش‌فرض " + strconv.Itoa(capacity) + ")"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			p.warn("ظرفیت باید عددی بزرگ‌تر از صفر باشد")
			return
		}
		capacity = n
	}

	pos, err := p.store.NextRoomPosition(ctx)
	if err != nil {
		p.warn("خطا: " + err.Error())
		return
	}

	err = p.store.CreateRoom(ctx, storage.Room{
		ID: storage.NewUUID(), Name: name, Password: password,
		Capacity: capacity, Position: pos,
	})
	if err != nil {
		p.warn("خطا: " + err.Error())
		return
	}
	p.okLive("روم ساخته شد")
}

func (p *Panel) editRoom(ctx context.Context, list []storage.Room) {
	room, ok := p.pickRoom("شمارهٔ روم برای ویرایش", list)
	if !ok {
		return
	}

	p.printf("\n  ویرایش «%s» — هر فیلد را خالی بگذارید تا تغییر نکند\n", room.Name)

	if raw := p.ask("نام جدید"); raw != "" {
		name, err := textutil.NormalizeName(raw, "نام روم", rooms.RoomNameMinLen, rooms.RoomNameMaxLen)
		if err != nil {
			p.warn(err.Error())
			return
		}
		room.Name = name
	}
	switch answer := p.ask("رمز جدید (برای حذف رمز عبارت - را بزنید)"); answer {
	case "": // unchanged
	case "-":
		room.Password = ""
	default:
		room.Password = answer
	}
	if raw := p.ask("ظرفیت جدید"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			p.warn("ظرفیت باید عددی بزرگ‌تر از صفر باشد")
			return
		}
		room.Capacity = n
	}

	if err := p.store.UpdateRoom(ctx, room); err != nil {
		p.warn("خطا: " + err.Error())
		return
	}
	p.okLive("ذخیره شد")
}

func (p *Panel) deleteRoom(ctx context.Context, list []storage.Room) {
	room, ok := p.pickRoom("شمارهٔ روم برای حذف", list)
	if !ok {
		return
	}
	p.printf("\n  روم «%s» و تمام محتوای آن حذف می‌شود.\n", room.Name)
	if p.ask("مطمئنید؟ (y/n)") != "y" {
		p.warn("لغو شد")
		return
	}
	if err := p.store.DeleteRoom(ctx, room.ID); err != nil {
		p.warn("خطا: " + err.Error())
		return
	}
	p.okLive("روم حذف شد")
}

// pickRoom asks for a row number from the listing.
func (p *Panel) pickRoom(prompt string, list []storage.Room) (storage.Room, bool) {
	if len(list) == 0 {
		p.warn("رومی وجود ندارد")
		return storage.Room{}, false
	}
	p.println("")
	raw := p.ask(prompt)
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > len(list) {
		p.warn("شمارهٔ نامعتبر")
		return storage.Room{}, false
	}
	return list[n-1], true
}

func (p *Panel) askRoomName(prompt string) (string, bool) {
	name, err := textutil.NormalizeName(p.ask(prompt), "نام روم",
		rooms.RoomNameMinLen, rooms.RoomNameMaxLen)
	if err != nil {
		p.warn(err.Error())
		return "", false
	}
	return name, true
}

func hasPassword(pw string) string {
	if pw == "" {
		return "ندارد"
	}
	return "دارد"
}
