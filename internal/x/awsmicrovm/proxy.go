package awsmicrovm

// ProxyAuthHeader is the header carrying a CreateMicrovmAuthToken token on
// requests to a MicroVM endpoint through the AWS proxy.
const ProxyAuthHeader = "X-aws-proxy-auth"

// ProxyForceH2Header, set to "true" on a request, makes the AWS proxy forward
// it to the MicroVM endpoint over unencrypted HTTP/2 rather than HTTP/1.1.
const ProxyForceH2Header = "X-aws-proxy-force-h2"
