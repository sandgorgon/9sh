// Package token defines the lexical tokens of the kyu language.
package token

type Kind int

const (
	ILLEGAL Kind = iota
	EOF

	IDENT
	INT
	FLOAT
	STRING
	DURATION
	PATH

	// operators and punctuation
	ASSIGN   // =
	DEFINE   // :=
	PIPE     // |
	PERCENT  // %  (external/legacy command sigil, only valid immediately before an IDENT)
	PLUS     // +
	MINUS    // -
	STAR     // *
	SLASH    // /
	MOD      // %  (modulo, only valid as an infix operator between values)
	EQ       // ==
	NEQ      // !=
	LT       // <
	GT       // >
	LE       // <=
	GE       // >=
	AND      // &&
	OR       // ||
	NOT      // !
	QUESTION // ?
	AMP      // &  (background job, reserved for Phase 2)
	AT       // @  (remote block, reserved for Phase 5)
	DOT      // .
	COMMA    // ,
	COLON    // :
	SEMI     // ;
	NEWLINE

	LPAREN   // (
	RPAREN   // )
	LBRACE   // {
	RBRACE   // }
	LBRACKET // [
	RBRACKET // ]

	// keywords
	TRUE
	FALSE
	NULL
	IF
	ELSE
	BIND // reserved for Phase 3, lexed now so later phases need no lexer changes
)

var keywords = map[string]Kind{
	"true":  TRUE,
	"false": FALSE,
	"null":  NULL,
	"if":    IF,
	"else":  ELSE,
	"bind":  BIND,
}

// LookupIdent returns the keyword Kind for ident, or IDENT if it is not a keyword.
func LookupIdent(ident string) Kind {
	if k, ok := keywords[ident]; ok {
		return k
	}
	return IDENT
}

type Token struct {
	Kind    Kind
	Literal string
	Line    int
	Col     int
}

func (k Kind) String() string {
	switch k {
	case ILLEGAL:
		return "ILLEGAL"
	case EOF:
		return "EOF"
	case IDENT:
		return "IDENT"
	case INT:
		return "INT"
	case FLOAT:
		return "FLOAT"
	case STRING:
		return "STRING"
	case DURATION:
		return "DURATION"
	case PATH:
		return "PATH"
	case ASSIGN:
		return "="
	case DEFINE:
		return ":="
	case PIPE:
		return "|"
	case PERCENT:
		return "%"
	case PLUS:
		return "+"
	case MINUS:
		return "-"
	case STAR:
		return "*"
	case SLASH:
		return "/"
	case MOD:
		return "%"
	case EQ:
		return "=="
	case NEQ:
		return "!="
	case LT:
		return "<"
	case GT:
		return ">"
	case LE:
		return "<="
	case GE:
		return ">="
	case AND:
		return "&&"
	case OR:
		return "||"
	case NOT:
		return "!"
	case QUESTION:
		return "?"
	case AMP:
		return "&"
	case AT:
		return "@"
	case DOT:
		return "."
	case COMMA:
		return ","
	case COLON:
		return ":"
	case SEMI:
		return ";"
	case NEWLINE:
		return "NEWLINE"
	case LPAREN:
		return "("
	case RPAREN:
		return ")"
	case LBRACE:
		return "{"
	case RBRACE:
		return "}"
	case LBRACKET:
		return "["
	case RBRACKET:
		return "]"
	case TRUE:
		return "true"
	case FALSE:
		return "false"
	case NULL:
		return "null"
	case IF:
		return "if"
	case ELSE:
		return "else"
	case BIND:
		return "bind"
	default:
		return "UNKNOWN"
	}
}
