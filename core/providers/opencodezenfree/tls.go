package opencodezenfree

import (
	"encoding/hex"
	"net"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/valyala/fasthttp"
)

// rawClientHelloHex captures the working TLS ClientHello from OpenCode CLI
// (Frame 96 from opencode-real-tls.pcap). It matches the JA3/JA4 fingerprint
// and cipher suite order accepted by Cloudflare edge for opencode.ai endpoints.
const rawClientHelloHex = "16030105d9010005d5030331c4617760cf2143673e7d43e8a094db41df49a6b980d397bd02ffe4ef51df0020fb88fac1e6cfd05f3dc38a17ebf7ba76a18ddc5d18c66bdb3aa96f43a250ec5d0022130113021303c02bc02fc02cc030cca9cca8c009c013c00ac014009c009d002f00350100056a00000010000e00000b6f70656e636f64652e616900170000ff01000100000a000a000811ec001d00170018000b00020100002300000010000b000908687474702f312e31000500050100000000000d001800160403080404010503080505010806060108070603020100120000003304ea04e811ec04c08998b69b00398464b67bc36388b03bc7a675c24a74c2d4712f298a18b4240d07959ce391de23709d0c2b7c3c166f65a37b97cc879bc98110381f22bec4817ad8d31aeebc1e1816bc480ad01fba55e4361d22d244364b0d5a8bc6d5718a48a62c54c8044be8a621eca408b03a45706fae1286cba70cbe24b1bf5711d154681a89598956579937b6810196869aa465e4498fc61b8bcc86d9222e34da5bac1b77870992bb923844c802319c7b8533b08834cd6ba8308e513ae9762cbadb089ab9b6c21864911a66bebca29a5c144b24457a1b2b749589f424ad54e98238f58f50ec83748b7fb7b62e4cdb3fd562a03e0c6bc6c99cb92c9e24f68702973eb3865dbdc2255f0776d8645abc4354eb1605d9e42311e5ad32a6a4235100dc289a0874018cd5c81a794a423cadf1e42d96b3595f62c4d1c6cef177687df820c676315858c9fb525ede706e3cb72056d073ae749f09d1b398e8b824a5bd2ef160d8408765780ce283a5b8006906e3798fb846703bcefb12c7d2f33cc46b08141155c34bb9b5624cd7772cf9744eb3e7bbda9c97a6dba6f4c429ec37140d9848231c11b8544993d58ecbf991c9b443aadbb942c8549b748189a591da0c898594443caab5d4559bfe8cae9f1052356b30db8096d527106c4c7fdb69cb9cc464fbc68e73263592c45f726754db972f16384fd70000f228201a45841d2447bec9697a907418fa1a512a4882e9390270967811b5474244b9654d62660829597970e77cfa43523214786d83669515ca62451c63eb5ebb3b2641086f7bd921c0d59fbd491d4ff40bc0282bc221264026a0e6058bc9acc021327e8bc306e31986665580959849f6daa9609a933d145f3f2a2a81794c23148f8157bfb9669484f423da32cda7b09b5fb17bdf285d7960baf41786a0c00dbb462030f38c0dbc680bf182701691e9e286d3bb5c34918e4b51ca3343b172a1b0b1827ff94a35b60924cb99ab4ddb2525664930822849765a35fa7f661abca1e3809e3a437a9b25d96155a26817d38044d2356be299c369f55c4f888a7aa32d999c9007c49423b9671abc850442105538252a856fd0d869de2ca74b22657d111445ecc710455272a40265469c83a18c589213b2fb07cc91bc878b38f34c7ee73c06fad668bbf31931574839650a6cd1281c956a3526cd5f28334d3b4b26f85c9370416ca2caad3b689fd458b2070243e59a97564d2ff93cfd752782043b0fa3084660140eb37317634eb5744802f7761e7b6a296226d90b530d83167dfbbac2aa4802c899571cc5a01253ba914c736a80bb2bc9779b55f767b6d0cb96921220478a751ab9a4a4bb95316b362a9069546c9ed20ba7521ab0d91c4895c1645a693aff55a40d21ad361c267d09b258565291f8ad2cf864ee3612d2a89900edb8d02cb3f99047971c3af5700582525702eb96be2a7221442f47e429c3b4aacf0200365653202c640f1951eddb4fb7c8ab9eab4e0f365a2af23d9320c21ae6b39fa94a33078c03087407890aabbc5ebbeb413ae4006841ad3554703e09c3c77c10a1b32b48046522461de6e0817e8c9c34442a8ec5b738c88638994caf66123745ae800a340c11508973883f983036205fb489557e05be5690c9457d779d376923bc95af106ecc1bcbeb105bb7bec45f59822f8526e94ec7100f56e6e3767731ed18641696a6f2293396a1308fe6572da7036858001d0020ec4ce8938d139a1f0d279ed940a7cd1bcc3e17cc49a42d3795e08c4129af5d5e002d00020101002b00050403040303"

var (
	rawClientHelloBytes []byte
	rawClientHelloErr   error
)

func init() {
	rawClientHelloBytes, rawClientHelloErr = hex.DecodeString(rawClientHelloHex)
}

// newClientHelloSpec parses the captured ClientHello bytes into a fresh
// utls.ClientHelloSpec. ClientHelloSpec contains private key shares and extension
// states that get consumed during the TLS handshake, so each connection requires
// a fresh specification to avoid "tls: internal error" on reuse.
func newClientHelloSpec() (*utls.ClientHelloSpec, error) {
	if rawClientHelloErr != nil {
		return nil, rawClientHelloErr
	}
	fp := &utls.Fingerprinter{AllowBluntMimicry: true}
	return fp.FingerprintClientHello(rawClientHelloBytes)
}

// isTLSTarget determines if addr requires TLS negotiation. Loopback addresses on non-443
// ports (such as httptest.NewServer instances in tests) are treated as plain TCP.
func isTLSTarget(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		port = "443"
	}
	if (host == "127.0.0.1" || host == "localhost" || host == "::1") && port != "443" {
		return false
	}
	return port == "443" || host == "opencode.ai" || strings.HasSuffix(host, ".opencode.ai")
}

// configureOpencodeZenFreeTLS configures a fasthttp.Client to perform TLS handshakes
// with the exact ClientHello specification expected by OpenCode's Cloudflare edge.
// Because fasthttp detects any net.Conn implementing interface{ Handshake() error }
// as already TLS-wrapped, it forwards the resulting uConn directly without standard
// crypto/tls re-wrapping.
func configureOpencodeZenFreeTLS(client *fasthttp.Client) *fasthttp.Client {
	if client == nil {
		return nil
	}

	baseDial := client.Dial
	baseDialTimeout := client.DialTimeout

	dialWrapper := func(addr string, timeout time.Duration) (net.Conn, error) {
		var rawConn net.Conn
		var dialErr error

		if baseDialTimeout != nil {
			rawConn, dialErr = baseDialTimeout(addr, timeout)
		} else if baseDial != nil {
			rawConn, dialErr = baseDial(addr)
		} else {
			rawConn, dialErr = net.DialTimeout("tcp", addr, timeout)
		}
		if dialErr != nil {
			return nil, dialErr
		}

		if !isTLSTarget(addr) {
			return rawConn, nil
		}

		host, _, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			host = addr
		}

		spec, specErr := newClientHelloSpec()
		if specErr != nil {
			rawConn.Close()
			return nil, specErr
		}

		uConn := utls.UClient(rawConn, &utls.Config{
			ServerName: host,
		}, utls.HelloCustom)

		if applyErr := uConn.ApplyPreset(spec); applyErr != nil {
			rawConn.Close()
			return nil, applyErr
		}

		if handshakeErr := uConn.Handshake(); handshakeErr != nil {
			rawConn.Close()
			return nil, handshakeErr
		}

		return uConn, nil
	}

	client.Dial = func(addr string) (net.Conn, error) {
		timeout := client.ReadTimeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		return dialWrapper(addr, timeout)
	}
	client.DialTimeout = dialWrapper

	return client
}
