// zk provides with higher level commands over the lower level zookeeper connector
package zk

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	gopath "path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/outbrain/golib/log"
	"github.com/samuel/go-zookeeper/zk"
)

type ZooKeeper struct {
	servers        []string
	authScheme     string
	authExpression []byte
	logging        bool

	// We assume complete access to all
	flags int32
	acl   []zk.ACL
}

func NewZooKeeper() *ZooKeeper {
	return &ZooKeeper{
		flags: int32(0),
		acl:   zk.WorldACL(zk.PermAll),
	}
}

// SetServers sets the list of servers for the zookeeper client to connect to.
// Each element in the array should be in either of following forms:
// - "servername"
// - "servername:port"
func (zook *ZooKeeper) SetServers(serversArray []string) {
	zook.servers = serversArray
}

func (zook *ZooKeeper) SetAuth(scheme string, auth []byte) {
	log.Debug("Setting Auth ")
	zook.authScheme = scheme
	zook.authExpression = auth
}

// SetFlags sets the Ephemeral flag (1)
func (zook *ZooKeeper) SetEphemeral() {
	zook.SetFlags(1)
}

// SetSequencial sets the Sequence flag (2)
func (zook *ZooKeeper) SetSequencial() {
	zook.SetFlags(2)
}

// SetFlags allows setting None (0) Ephemeral (1) or Sequencial Values (2)
func (zook *ZooKeeper) SetFlags(flagVal int32) {
	match := true
	switch match {
	case flagVal > 2 || flagVal < 0:
		zook.flags = 0
	default:
		zook.flags = flagVal
	}
}

// Returns acls
func (zook *ZooKeeper) BuildACL(authScheme string, user string, pwd string, acls string) (perms []zk.ACL, err error) {
	aclsList := strings.Split(acls, ",")
	for _, elem := range aclsList {
		acl, err := strconv.ParseInt(elem, 10, 32)
		if err != nil {
			break
		}
		perm := zk.DigestACL(int32(acl), user, pwd)
		perms = append(perms, perm[0])
	}
	return perms, err
}

type infoLogger struct{}

func (_ infoLogger) Printf(format string, a ...interface{}) {
	log.Infof(format, a...)
}

// connect
func (zook *ZooKeeper) connect() (*zk.Conn, error) {
	var conn *zk.Conn
	var errd error
	if zook.logging {
		conn, _, errd = zk.Connect(zook.servers, time.Second, zk.WithLogInfo(true))
	} else {
		conn, _, errd = zk.Connect(zook.servers, time.Second, zk.WithLogInfo(false))
	}
	if errd == nil && zook.authScheme != "" {
		log.Debugf("Add Auth %s %s", zook.authScheme, zook.authExpression)
		errd = conn.AddAuth(zook.authScheme, zook.authExpression)
	}

	return conn, errd
}

// EnableLogger toggles the logger
func (zook *ZooKeeper) EnableLogger(enable bool) {
	if enable {
		zook.logging = true
		return
	}
}

// Exists returns true when the given path exists
func (zook *ZooKeeper) Exists(path string) (bool, error) {
	connection, err := zook.connect()
	if err != nil {
		return false, err
	}
	defer connection.Close()

	exists, _, err := connection.Exists(path)
	return exists, err
}

// Get returns value associated with given path, or error if path does not exist
func (zook *ZooKeeper) Get(path string) ([]byte, error) {
	connection, err := zook.connect()
	if err != nil {
		return []byte{}, err
	}
	defer connection.Close()

	data, _, err := connection.Get(path)
	return data, err
}

func (zook *ZooKeeper) GetACL(path string) (data []string, err error) {
	connection, err := zook.connect()
	if err != nil {
		return nil, err
	}
	defer connection.Close()

	perms, _, err := connection.GetACL(path)
	return zook.aclsToString(perms), err
}

func (zook *ZooKeeper) aclsToString(acls []zk.ACL) (result []string) {
	for _, acl := range acls {
		var buffer bytes.Buffer

		buffer.WriteString(fmt.Sprintf("%v:%v:", acl.Scheme, acl.ID))

		if acl.Perms&zk.PermCreate != 0 {
			buffer.WriteString("c")
		}
		if acl.Perms&zk.PermDelete != 0 {
			buffer.WriteString("d")
		}
		if acl.Perms&zk.PermRead != 0 {
			buffer.WriteString("r")
		}
		if acl.Perms&zk.PermWrite != 0 {
			buffer.WriteString("w")
		}
		if acl.Perms&zk.PermAdmin != 0 {
			buffer.WriteString("a")
		}
		result = append(result, buffer.String())
	}
	return result
}

func (zook *ZooKeeper) HasChildren(path string) (bool, error) {
	connection, err := zook.connect()
	if err != nil {
		return true, err
	}
	defer connection.Close()

	children, _, err := connection.Children(path)
	return (len(children) > 0), err
}

// Children returns sub-paths of given path, optionally empty array, or error if path does not exist
func (zook *ZooKeeper) Children(path string) ([]string, error) {
	connection, err := zook.connect()
	if err != nil {
		return []string{}, err
	}
	defer connection.Close()

	children, _, err := connection.Children(path)
	return children, err
}

// childrenRecursiveInternal: internal implementation of recursive-children query.
func (zook *ZooKeeper) childrenRecursiveInternal(connection *zk.Conn, path string, incrementalPath string) ([]string, error) {
	children, _, err := connection.Children(path)
	if err != nil {
		return children, err
	}
	sort.Sort(sort.StringSlice(children))
	recursiveChildren := []string{}
	for _, child := range children {
		incrementalChild := gopath.Join(incrementalPath, child)
		recursiveChildren = append(recursiveChildren, incrementalChild)
		if zook.logging {
			log.Debugf("incremental child: %+v", incrementalChild)
		}
		incrementalChildren, err := zook.childrenRecursiveInternal(connection, gopath.Join(path, child), incrementalChild)
		if err != nil {
			return children, err
		}
		recursiveChildren = append(recursiveChildren, incrementalChildren...)
	}
	return recursiveChildren, err
}

// ChildrenRecursive returns list of all descendants of given path (optionally empty), or error if the path
// does not exist.
// Every element in result list is a relative subpath for the given path.
func (zook *ZooKeeper) ChildrenRecursive(path string) ([]string, error) {
	connection, err := zook.connect()
	if err != nil {
		return []string{}, err
	}
	defer connection.Close()

	result, err := zook.childrenRecursiveInternal(connection, path, "")
	return result, err
}

// createInternal: create a new path
func (zook *ZooKeeper) createInternal(connection *zk.Conn, path string, data []byte, acl []zk.ACL, force bool) (string, error) {
	if path == "/" {
		return "/", nil
	}
	if zook.logging {
		log.Debugf("creating: %s", path)
	}
	attempts := 0
	for {
		attempts += 1
		returnValue, err := connection.Create(path, data, zook.flags, zook.acl)
		if zook.logging {
			log.Debugf("create status for %s: %s, %+v", path, returnValue, err)
		}
		if err != nil && force && attempts < 2 {
			parentPath := gopath.Dir(path)
			if parentPath == path {
				return returnValue, err
			}
			returnValue, err = zook.createInternal(connection, parentPath, []byte("recurse auto-generated"), acl, force)
		} else {
			return returnValue, err
		}
	}
	return "", nil
}

// createInternalWithACL: create a new path with acl
func (zook *ZooKeeper) createInternalWithACL(connection *zk.Conn, path string, data []byte, force bool, perms []zk.ACL) (string, error) {
	if path == "/" {
		return "/", nil
	}
	log.Debugf("creating: %s with acl ", path)
	attempts := 0
	for {
		attempts += 1
		returnValue, err := connection.Create(path, data, zook.flags, perms)
		log.Debugf("create status for %s: %s, %+v", path, returnValue, err)
		if err != nil && force && attempts < 2 {
			returnValue, err = zook.createInternalWithACL(connection, gopath.Dir(path), []byte("zookeepercli auto-generated"), force, perms)
		} else {
			return returnValue, err
		}
	}
	return "", nil
}

// Create will create a new path, or exit with error should the path exist.
// The "force" param controls the behavior when path's parent directory does not exist.
// When "force" is false, the function returns with error/ When "force" is true, it recursively
// attempts to create required parent directories.
func (zook *ZooKeeper) Create(path string, data []byte, aclstr string, force bool) (string, error) {
	connection, err := zook.connect()
	if err != nil {
		return "", err
	}
	defer connection.Close()

	if len(aclstr) > 0 {
		zook.acl, err = zook.parseACLString(aclstr)
		if err != nil {
			return "", err
		}
	}

	return zook.createInternal(connection, path, data, zook.acl, force)
}

func (zook *ZooKeeper) CreateWithACL(path string, data []byte, force bool, perms []zk.ACL) (string, error) {
	connection, err := zook.connect()
	if err != nil {
		return "", err
	}
	defer connection.Close()

	return zook.createInternalWithACL(connection, path, data, force, perms)
}

// Set updates a value for a given path, or returns with error if the path does not exist
func (zook *ZooKeeper) Set(path string, data []byte) (*zk.Stat, error) {
	connection, err := zook.connect()
	if err != nil {
		return nil, err
	}
	defer connection.Close()

	return connection.Set(path, data, -1)
}

// updates the ACL on a given path
func (zook *ZooKeeper) SetACL(path string, aclstr string, force bool) (string, error) {
	connection, err := zook.connect()
	if err != nil {
		return "", err
	}
	defer connection.Close()

	acl, err := zook.parseACLString(aclstr)
	if err != nil {
		return "", err
	}

	if force {
		exists, _, err := connection.Exists(path)
		if err != nil {
			return "", err
		}

		if !exists {
			return zook.createInternal(connection, path, []byte(""), acl, force)
		}
	}

	_, err = connection.SetACL(path, acl, -1)
	return path, err
}

func (zook *ZooKeeper) parseACLString(aclstr string) (acl []zk.ACL, err error) {
	aclsList := strings.Split(aclstr, ",")
	for _, entry := range aclsList {
		parts := strings.Split(entry, ":")
		var scheme, id string
		var perms int32
		if len(parts) > 3 && parts[0] == "digest" {
			scheme = parts[0]
			id = fmt.Sprintf("%s:%s", parts[1], parts[2])
			perms, err = zook.parsePermsString(parts[3])
		} else {
			scheme, id = parts[0], parts[1]
			perms, err = zook.parsePermsString(parts[2])
		}

		if err == nil {
			perm := zk.ACL{Scheme: scheme, ID: id, Perms: perms}
			acl = append(acl, perm)
		}
	}
	return acl, err
}

func (zook *ZooKeeper) parsePermsString(permstr string) (perms int32, err error) {
	if x, e := strconv.ParseFloat(permstr, 64); e == nil {
		perms = int32(math.Min(x, 31))
	} else {
		for _, rune := range strings.Split(permstr, "") {
			switch rune {
			case "r":
				perms |= zk.PermRead
				break
			case "w":
				perms |= zk.PermWrite
				break
			case "c":
				perms |= zk.PermCreate
				break
			case "d":
				perms |= zk.PermDelete
				break
			case "a":
				perms |= zk.PermAdmin
				break
			default:
				err = errors.New("invalid ACL string specified")
			}

			if err != nil {
				break
			}
		}
	}
	return perms, err
}

// Delete removes a path entry. It exits with error if the path does not exist, or has subdirectories.
func (zook *ZooKeeper) Delete(path string) error {
	connection, err := zook.connect()
	if err != nil {
		return err
	}
	defer connection.Close()
	return connection.Delete(path, -1)
}

// Delete recursive if has subdirectories.
func (zook *ZooKeeper) DeleteRecursive(path string) error {
	result, err := zook.ChildrenRecursive(path)
	if err != nil {
		log.Fatale(err)
	}
	for i := len(result) - 1; i >= 0; i-- {
		znode := path + "/" + result[i]
		if err = zook.Delete(znode); err != nil {
			log.Fatale(err)
		}
	}
	return zook.Delete(path)
}
