package stratum

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// newLineScanner 行扫描器（抗超大行洪水，各方言共用）。
func newLineScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	return sc
}

// parseHexU32 解析 8 hex 字符（大端）为 uint32。stratum 的 ntime/nonce/version_bits 均此格式。
func parseHexU32(s string) (uint32, error) {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(s) == 0 || len(s) > 8 {
		return 0, fmt.Errorf("非法 u32 hex: %q", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

// u32hex 编码为 8 hex 字符大端（notify 的 version/bits/ntime）。
func u32hex(v uint32) string {
	return fmt.Sprintf("%08x", v)
}

func remoteHost(conn net.Conn) string {
	host, _, err := net.SplitHostPort(conn.RemoteAddr().String())
	if err != nil {
		return conn.RemoteAddr().String()
	}
	return host
}

// marshalStratum 序列化一条 stratum 消息，保证响应类（非 method）result+error+id 三件套齐全。
// NiceHash pool verificator 因缺 "error":null 判不合格，故显式补齐。
func marshalStratum(m rpcMsg) ([]byte, error) {
	if m.Method != "" {
		// 请求/通知：{id, method, params}
		return json.Marshal(struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}{m.ID, m.Method, m.Params})
	}
	// 响应：{id, result, error}——result/error 即便为 nil 也输出显式 null
	return json.Marshal(struct {
		ID     json.RawMessage `json:"id"`
		Result any             `json:"result"`
		Error  any             `json:"error"`
	}{m.ID, m.Result, m.Error})
}
