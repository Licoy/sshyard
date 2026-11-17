package sftpsvc

import (
	"bytes"
	"os/user"
	"strconv"
	"sync"
)

// namesLimit is the most of /etc/passwd or /etc/group Names reads.
const namesLimit = 1 << 20

// Names are the server's user and group names, by ID, from its
// /etc/passwd and /etc/group: empty when it has none to read (an ID is
// then shown as a number). The names are the server's: show them as
// untrusted text.
func (c *Client) Names() (users, groups map[uint32]string) {
	read := func(p string) map[uint32]string {
		f, err := c.c.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		data, _ := readSmall(f, namesLimit)
		return parseIDs(data)
	}
	return read("/etc/passwd"), read("/etc/group")
}

// parseIDs reads the names of lines "name:x:id:…", as /etc/passwd and
// /etc/group have them; the first name of an ID wins.
func parseIDs(data []byte) map[uint32]string {
	out := map[uint32]string{}
	for line := range bytes.Lines(data) {
		f := bytes.SplitN(bytes.TrimRight(line, "\r\n"), []byte(":"), 4)
		if len(f) < 3 || len(f[0]) == 0 || f[0][0] == '#' {
			continue
		}
		id, err := strconv.ParseUint(string(f[2]), 10, 32)
		if err != nil {
			continue
		}
		if _, ok := out[uint32(id)]; !ok {
			out[uint32(id)] = string(f[0])
		}
	}
	return out
}

// localNames caches LocalUser and LocalGroup.
var localNames struct {
	sync.Mutex
	users, groups map[uint32]string
}

// LocalUser is the name of the local user uid, its number when it has
// none.
func LocalUser(uid uint32) string {
	return localName(&localNames.users, uid, func(id string) (string, error) {
		u, err := user.LookupId(id)
		if err != nil {
			return "", err
		}
		return u.Username, nil
	})
}

// LocalGroup is the name of the local group gid, its number when it has
// none.
func LocalGroup(gid uint32) string {
	return localName(&localNames.groups, gid, func(id string) (string, error) {
		g, err := user.LookupGroupId(id)
		if err != nil {
			return "", err
		}
		return g.Name, nil
	})
}

func localName(cache *map[uint32]string, id uint32, look func(string) (string, error)) string {
	localNames.Lock()
	defer localNames.Unlock()
	if n, ok := (*cache)[id]; ok {
		return n
	}
	s := strconv.FormatUint(uint64(id), 10)
	if n, err := look(s); err == nil && n != "" {
		s = n
	}
	if *cache == nil {
		*cache = map[uint32]string{}
	}
	(*cache)[id] = s
	return s
}
