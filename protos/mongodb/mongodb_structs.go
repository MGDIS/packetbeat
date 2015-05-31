package mongodb

// Represent a mongodb message being parsed
import (
	"packetbeat/common"
	"time"
)

type MongodbMessage struct {
	Ts time.Time

	TcpTuple     common.TcpTuple
	CmdlineTuple *common.CmdlineTuple
	Direction    uint8

	IsResponse      bool
	ExpectsResponse bool

	// Standard message header fields from mongodb wire protocol
	// see http://docs.mongodb.org/meta-driver/latest/legacy/mongodb-wire-protocol/#standard-message-header
	messageLength int
	requestId     int
	responseTo    int
	opCode        string

	// deduced from content. Either an operation from the original wire protocol or the name of a command (passed through a query)
	// List of commands: http://docs.mongodb.org/manual/reference/command/
	// List of original protocol operations: http://docs.mongodb.org/meta-driver/latest/legacy/mongodb-wire-protocol/#request-opcodes
	method string
	error  string

	// Other fields vary very much depending on operation type
	// lets just put them in a map
	event common.MapStr
}

// Represent a stream being parsed that contains a mongodb message
type MongodbStream struct {
	tcptuple *common.TcpTuple

	data []byte

	parseOffset   int
	bytesReceived int

	message *MongodbMessage
}

// Parser moves to next message in stream
func (stream *MongodbStream) PrepareForNewMessage() {
	stream.data = stream.data[stream.message.messageLength:]
	stream.message = &MongodbMessage{Ts: stream.message.Ts}
}

// The private data of a parser instance
// is composed of 2 potentially active streams: incoming, outgoing
type mongodbPrivateData struct {
	Data [2]*MongodbStream
}

// Represent a full mongodb transaction (request/reply)
// These transactions are the end product of this parser
type MongodbTransaction struct {
	Type         string
	tuple        common.TcpTuple
	cmdline      *common.CmdlineTuple
	Src          common.Endpoint
	Dst          common.Endpoint
	ResponseTime int32
	Ts           int64
	JsTs         time.Time
	ts           time.Time
	BytesOut     int
	BytesIn      int
	timer        *time.Timer

	Mongodb common.MapStr

	event  common.MapStr
	method string
	error  string
}

const (
	TransactionsHashSize = 2 ^ 16
	TransactionTimeout   = 10 * 1e9
)

// List of valid mongodb wire protocol operation codes
// see http://docs.mongodb.org/meta-driver/latest/legacy/mongodb-wire-protocol/#request-opcodes
var OpCodes = map[int]string{
	1:    "OP_REPLY",
	1000: "OP_MSG",
	2001: "OP_UPDATE",
	2002: "OP_INSERT",
	2003: "RESERVED",
	2004: "OP_QUERY",
	2005: "OP_GET_MORE",
	2006: "OP_DELETE",
	2007: "OP_KILL_CURSORS",
}

// List of mongodb user commands (send throuwh a query of the legacy protocol)
// see http://docs.mongodb.org/manual/reference/command/
var UserCommands = []string{
	"aggregate",
	"count",
	"distinct",
	"group",
	"mapReduce",
	"geoNear",
	"geoSearch",
	"geoWalk",
	"insert",
	"update",
	"delete",
	"findAndModify",
	"getLastError",
	"getPrevError",
	"resetError",
	"eval",
	"parallelCollectionScan",
	"planCacheListFilters",
	"planCacheSetFilter",
	"planCacheClearFilters",
	"planCacheListQueryShapes",
	"planCacheListPlans",
	"planCacheClear",
}
