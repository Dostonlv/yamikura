package yamikura

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ShardCount = 1024
)

type CacheItem struct {
	Value      interface{}
	Expiration int64
}

type Shard struct {
	items map[string]CacheItem
	mu    sync.RWMutex
}

type Cache struct {
	shards    [ShardCount]*Shard
	janitor   *janitor
	itemCount int64
	prefix    *PrefixTree
	prefixMu  sync.RWMutex
}

type PrefixTree struct {
	children map[byte]*PrefixTree
	keys     map[string]bool
}

type Server struct {
	cache    *Cache
	listener net.Listener
}

type Request struct {
	Command string
	Args    []string
}

type Response struct {
	Type  byte
	Value any
}

var (
	itemPool = sync.Pool{
		New: func() any {
			return &CacheItem{}
		},
	}

	bufferPool = sync.Pool{
		New: func() any {
			return bytes.NewBuffer(make([]byte, 0, 4096))
		},
	}
)

func New() *Cache {
	c := &Cache{
		prefix: &PrefixTree{
			children: make(map[byte]*PrefixTree),
			keys:     make(map[string]bool),
		},
	}

	for i := 0; i < ShardCount; i++ {
		c.shards[i] = &Shard{
			items: make(map[string]CacheItem),
		}
	}

	j := &janitor{
		interval: 1 * time.Minute,
		stop:     make(chan bool),
	}
	c.janitor = j
	go j.Run(c)

	return c
}

func StartServer(addr string, c *Cache) (*Server, error) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}

	server := &Server{
		cache:    c,
		listener: listener,
	}

	go server.serve()

	return server, nil
}

func StartTLSServer(addr string, certFile, keyFile string, c *Cache) (*Server, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	listener, err := tls.Listen("tcp", addr, tlsConfig)
	if err != nil {
		return nil, err
	}

	server := &Server{
		cache:    c,
		listener: listener,
	}

	go server.serve()

	return server, nil
}

func (c *Cache) Set(key string, value any, ttl time.Duration) {
	shard := c.getShard(key)

	var exp int64
	if ttl > 0 {
		exp = time.Now().Add(ttl).UnixNano()
	}

	shard.mu.Lock()
	_, exists := shard.items[key]
	shard.items[key] = CacheItem{
		Value:      value,
		Expiration: exp,
	}
	shard.mu.Unlock()

	if !exists {
		atomic.AddInt64(&c.itemCount, 1)
		c.updatePrefixTree(key, true)
	}
}

func (c *Cache) Get(key string) (any, bool) {
	shard := c.getShard(key)

	shard.mu.RLock()
	item, found := shard.items[key]
	shard.mu.RUnlock()

	if !found {
		return nil, false
	}

	if item.Expiration > 0 && item.Expiration < time.Now().UnixNano() {
		c.Del(key)
		return nil, false
	}

	return item.Value, true
}

func (c *Cache) Del(key string) bool {
	shard := c.getShard(key)

	shard.mu.Lock()
	_, found := shard.items[key]
	if found {
		delete(shard.items, key)
		atomic.AddInt64(&c.itemCount, -1)
		c.updatePrefixTree(key, false)
	}
	shard.mu.Unlock()

	return found
}

func (c *Cache) TTL(key string) time.Duration {
	shard := c.getShard(key)

	shard.mu.RLock()
	item, found := shard.items[key]
	shard.mu.RUnlock()

	if !found {
		return -2 * time.Second
	}

	if item.Expiration == 0 {
		return -1 * time.Second
	}

	ttl := time.Duration(item.Expiration - time.Now().UnixNano())
	if ttl < 0 {
		return -2 * time.Second
	}

	return ttl / time.Nanosecond
}

func (c *Cache) Keys(pattern string) []string {
	if pattern == "*" {
		return c.getAllKeys()
	}

	isSimplePrefix := strings.HasSuffix(pattern, "*") &&
		!strings.Contains(pattern[:len(pattern)-1], "*") &&
		!strings.Contains(pattern, "?") &&
		!strings.Contains(pattern, "[")

	if isSimplePrefix {
		prefix := pattern[:len(pattern)-1]
		return c.getKeysByPrefix(prefix)
	}

	return c.getKeysByPattern(pattern)
}

func (c *Cache) getShard(key string) *Shard {
	h := fnv64a(key)
	return c.shards[h%ShardCount]
}

func (c *Cache) updatePrefixTree(key string, add bool) {
	c.prefixMu.Lock()
	defer c.prefixMu.Unlock()

	if add {
		node := c.prefix
		for i := 0; i < len(key); i++ {
			if node.children[key[i]] == nil {
				node.children[key[i]] = &PrefixTree{
					children: make(map[byte]*PrefixTree),
					keys:     make(map[string]bool),
				}
			}
			node = node.children[key[i]]
		}
		node.keys[key] = true
	} else {
		node := c.prefix
		var path []*PrefixTree

		for i := 0; i < len(key); i++ {
			if node.children[key[i]] == nil {
				return
			}
			path = append(path, node)
			node = node.children[key[i]]
		}

		delete(node.keys, key)

		for i := len(key) - 1; i >= 0; i-- {
			parent := path[i]
			child := parent.children[key[i]]

			if len(child.keys) == 0 && len(child.children) == 0 {
				delete(parent.children, key[i])
			} else {
				break
			}
		}
	}
}

func (c *Cache) getAllKeys() []string {
	var keys []string

	for i := 0; i < ShardCount; i++ {
		shard := c.shards[i]
		shard.mu.RLock()
		for key := range shard.items {
			keys = append(keys, key)
		}
		shard.mu.RUnlock()
	}

	return keys
}

func (c *Cache) getKeysByPrefix(prefix string) []string {
	c.prefixMu.RLock()
	defer c.prefixMu.RUnlock()

	node := c.prefix
	for i := 0; i < len(prefix); i++ {
		if node.children[prefix[i]] == nil {
			return nil
		}
		node = node.children[prefix[i]]
	}

	var keys []string
	for key := range node.keys {
		keys = append(keys, key)
	}

	return keys
}

func (c *Cache) getKeysByPattern(pattern string) []string {
	regexPattern := globToRegexp(pattern)
	re, err := regexp.Compile(regexPattern)
	if err != nil {
		return nil
	}

	var keys []string

	for i := 0; i < ShardCount; i++ {
		shard := c.shards[i]
		shard.mu.RLock()
		for key := range shard.items {
			if re.MatchString(key) {
				keys = append(keys, key)
			}
		}
		shard.mu.RUnlock()
	}

	return keys
}

func fnv64a(key string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	hash := uint64(offset64)
	for i := 0; i < len(key); i++ {
		hash ^= uint64(key[i])
		hash *= prime64
	}
	return hash
}

func globToRegexp(pattern string) string {
	var buffer strings.Builder
	buffer.WriteString("^")

	for i := 0; i < len(pattern); i++ {
		char := pattern[i]
		switch char {
		case '*':
			buffer.WriteString(".*")
		case '?':
			buffer.WriteString(".")
		case '[':
			buffer.WriteString("[")
		case ']':
			buffer.WriteString("]")
		case '\\':
			buffer.WriteString("\\\\")
		case '.', '+', '(', ')', '|', '{', '}', '^', '$':
			buffer.WriteString("\\")
			buffer.WriteRune(rune(char))
		default:
			buffer.WriteRune(rune(char))
		}
	}

	buffer.WriteString("$")
	return buffer.String()
}

type janitor struct {
	interval time.Duration
	stop     chan bool
}

func (j *janitor) Run(c *Cache) {
	ticker := time.NewTicker(j.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.DeleteExpired()
		case <-j.stop:
			return
		}
	}
}

func (c *Cache) DeleteExpired() {
	now := time.Now().UnixNano()

	for i := 0; i < ShardCount; i++ {
		shard := c.shards[i]
		var keysToDelete []string

		shard.mu.RLock()
		for k, v := range shard.items {
			if v.Expiration > 0 && v.Expiration < now {
				keysToDelete = append(keysToDelete, k)
			}
		}
		shard.mu.RUnlock()

		if len(keysToDelete) > 0 {
			shard.mu.Lock()
			for _, k := range keysToDelete {
				if v, exists := shard.items[k]; exists && v.Expiration > 0 && v.Expiration < now {
					delete(shard.items, k)
					atomic.AddInt64(&c.itemCount, -1)
					c.updatePrefixTree(k, false)
				}
			}
			shard.mu.Unlock()
		}
	}
}

func (s *Server) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			continue
		}

		go s.handleConnection(conn)
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	for {
		req, err := parseRequest(reader)
		if err != nil {
			return
		}

		resp := s.executeCommand(req)

		err = writeResponse(writer, resp)
		if err != nil {
			return
		}

		writer.Flush()
	}
}

func parseRequest(reader *bufio.Reader) (Request, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return Request{}, err
	}

	if len(line) == 0 || line[0] != '*' {
		return Request{}, fmt.Errorf("invalid protocol")
	}

	numArgs, err := strconv.Atoi(strings.TrimSpace(line[1:]))
	if err != nil {
		return Request{}, err
	}

	args := make([]string, numArgs)

	for i := 0; i < numArgs; i++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			return Request{}, err
		}

		if len(line) == 0 || line[0] != '$' {
			return Request{}, fmt.Errorf("invalid protocol")
		}

		argLen, err := strconv.Atoi(strings.TrimSpace(line[1:]))
		if err != nil {
			return Request{}, err
		}

		arg := make([]byte, argLen)
		_, err = reader.Read(arg)
		if err != nil {
			return Request{}, err
		}

		args[i] = string(arg)

		reader.ReadString('\n')
	}

	if len(args) == 0 {
		return Request{}, fmt.Errorf("no command")
	}

	return Request{
		Command: strings.ToUpper(args[0]),
		Args:    args[1:],
	}, nil
}

func (s *Server) executeCommand(req Request) Response {
	switch req.Command {
	case "PING":
		return Response{Type: '+', Value: "PONG"}
	case "GET":
		if len(req.Args) != 1 {
			return Response{Type: '-', Value: "ERR wrong number of arguments for 'get' command"}
		}
		value, found := s.cache.Get(req.Args[0])
		if !found {
			return Response{Type: '_', Value: nil}
		}
		return Response{Type: '+', Value: value}
	case "SET":
		if len(req.Args) < 2 {
			return Response{Type: '-', Value: "ERR wrong number of arguments for 'set' command"}
		}
		ttl := time.Duration(0)
		if len(req.Args) > 2 && req.Args[2] == "EX" {
			ex, err := strconv.Atoi(req.Args[3])
			if err != nil {
				return Response{Type: '-', Value: "ERR invalid expire time"}
			}
			ttl = time.Duration(ex) * time.Second
		}
		s.cache.Set(req.Args[0], req.Args[1], ttl)
		return Response{Type: '+', Value: "OK"}
	case "DEL":
		if len(req.Args) != 1 {
			return Response{Type: '-', Value: "ERR wrong number of arguments for 'del' command"}
		}
		deleted := s.cache.Del(req.Args[0])
		if deleted {
			return Response{Type: ':', Value: 1}
		}
		return Response{Type: ':', Value: 0}
	case "KEYS":
		if len(req.Args) != 1 {
			return Response{Type: '-', Value: "ERR wrong number of arguments for 'keys' command"}
		}
		keys := s.cache.Keys(req.Args[0])
		return Response{Type: '*', Value: keys}
	case "TTL":
		if len(req.Args) != 1 {
			return Response{Type: '-', Value: "ERR wrong number of arguments for 'ttl' command"}
		}
		ttl := s.cache.TTL(req.Args[0])
		seconds := int(ttl / time.Second)
		return Response{Type: ':', Value: seconds}
	default:
		return Response{Type: '-', Value: fmt.Sprintf("ERR unknown command '%s'", req.Command)}
	}
}

func writeResponse(writer *bufio.Writer, resp Response) error {
	switch resp.Type {
	case '+':
		writer.WriteByte('+')
		fmt.Fprintf(writer, "%v\r\n", resp.Value)
	case '-':
		writer.WriteByte('-')
		fmt.Fprintf(writer, "%s\r\n", resp.Value)
	case ':':
		writer.WriteByte(':')
		fmt.Fprintf(writer, "%d\r\n", resp.Value)
	case '$':
		s, ok := resp.Value.(string)
		if !ok {
			s = fmt.Sprintf("%v", resp.Value)
		}
		writer.WriteByte('$')
		fmt.Fprintf(writer, "%d\r\n%s\r\n", len(s), s)
	case '*':
		arr, ok := resp.Value.([]string)
		if !ok {
			return fmt.Errorf("invalid array value")
		}
		writer.WriteByte('*')
		fmt.Fprintf(writer, "%d\r\n", len(arr))
		for _, item := range arr {
			writer.WriteByte('$')
			fmt.Fprintf(writer, "%d\r\n%s\r\n", len(item), item)
		}
	case '_':
		writer.WriteString("$-1\r\n")
	}

	return nil
}
