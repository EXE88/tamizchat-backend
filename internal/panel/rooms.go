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
			p.warn("Could not read rooms: " + err.Error())
			return
		}

		p.clear()
		p.banner()
		p.printf("  %s (%d)\n\n", bold("Rooms"), len(list))

		if len(list) == 0 {
			p.println("  No rooms have been created yet.\n")
		} else {
			p.printf("  %-4s %-28s %-10s %-8s %s\n", "#", "NAME", "PASSWORD", "CAPACITY", "CREATED")
			for i, r := range list {
				p.printf("  %-4d %-28s %-10s %-8d %s\n", i+1, truncate(r.Name, 28),
					hasPassword(r.Password), r.Capacity,
					time.Unix(r.CreatedAt, 0).Format("2006-01-02"))
			}
			p.println("")
		}

		p.println("  n) Create a room")
		p.println("  e) Edit a room")
		p.println("  d) Delete a room")
		p.println("  0) Back\n")

		switch p.ask("Choose") {
		case "n":
			p.createRoom(ctx)
		case "e":
			p.editRoom(ctx, list)
		case "d":
			p.deleteRoom(ctx, list)
		case "0", "":
			return
		default:
			p.warn("Invalid choice")
		}
	}
}

func (p *Panel) createRoom(ctx context.Context) {
	p.println("")
	name, ok := p.askRoomName("Room name")
	if !ok {
		return
	}

	password := p.ask("Room password (empty = no password)")

	capacity := p.cfg.Int(config.KeyRoomsDefaultMaxUsers)
	if raw := p.ask("Capacity (empty = default " + strconv.Itoa(capacity) + ")"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			p.warn("Capacity must be a number greater than zero")
			return
		}
		capacity = n
	}

	pos, err := p.store.NextRoomPosition(ctx)
	if err != nil {
		p.warn("Error: " + err.Error())
		return
	}

	err = p.store.CreateRoom(ctx, storage.Room{
		ID: storage.NewUUID(), Name: name, Password: password,
		Capacity: capacity, Position: pos,
	})
	if err != nil {
		p.warn("Error: " + err.Error())
		return
	}
	p.okLive("Room created")
}

func (p *Panel) editRoom(ctx context.Context, list []storage.Room) {
	room, ok := p.pickRoom("Number of the room to edit", list)
	if !ok {
		return
	}

	p.printf("\n  Editing \"%s\" — leave a field empty to keep it unchanged\n", room.Name)

	if raw := p.ask("New name"); raw != "" {
		name, err := textutil.NormalizeName(raw, "room name", rooms.RoomNameMinLen, rooms.RoomNameMaxLen)
		if err != nil {
			p.warn(err.Error())
			return
		}
		room.Name = name
	}
	switch answer := p.ask("New password (enter - to remove the password)"); answer {
	case "": // unchanged
	case "-":
		room.Password = ""
	default:
		room.Password = answer
	}
	if raw := p.ask("New capacity"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			p.warn("Capacity must be a number greater than zero")
			return
		}
		room.Capacity = n
	}

	if err := p.store.UpdateRoom(ctx, room); err != nil {
		p.warn("Error: " + err.Error())
		return
	}
	p.okLive("Saved")
}

func (p *Panel) deleteRoom(ctx context.Context, list []storage.Room) {
	room, ok := p.pickRoom("Number of the room to delete", list)
	if !ok {
		return
	}
	p.printf("\n  Room \"%s\" and everything in it will be deleted.\n", room.Name)
	if p.ask("Are you sure? (y/n)") != "y" {
		p.warn("Cancelled")
		return
	}
	if err := p.store.DeleteRoom(ctx, room.ID); err != nil {
		p.warn("Error: " + err.Error())
		return
	}
	p.okLive("Room deleted")
}

// pickRoom asks for a row number from the listing.
func (p *Panel) pickRoom(prompt string, list []storage.Room) (storage.Room, bool) {
	if len(list) == 0 {
		p.warn("There are no rooms")
		return storage.Room{}, false
	}
	p.println("")
	raw := p.ask(prompt)
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > len(list) {
		p.warn("Invalid number")
		return storage.Room{}, false
	}
	return list[n-1], true
}

func (p *Panel) askRoomName(prompt string) (string, bool) {
	name, err := textutil.NormalizeName(p.ask(prompt), "room name",
		rooms.RoomNameMinLen, rooms.RoomNameMaxLen)
	if err != nil {
		p.warn(err.Error())
		return "", false
	}
	return name, true
}

func hasPassword(pw string) string {
	if pw == "" {
		return "no"
	}
	return "yes"
}
