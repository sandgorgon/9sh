package parser

import (
	"testing"

	"github.com/sandgorgon/9sh/kyu/ast"
	"github.com/sandgorgon/9sh/kyu/token"
)

func parseOK(t *testing.T, src string) *ast.Program {
	t.Helper()
	p := New(src)
	prog := p.ParseProgram()
	if len(p.Errors()) > 0 {
		t.Fatalf("src %q: parse errors: %v", src, p.Errors())
	}
	return prog
}

func TestDefineAndAssign(t *testing.T) {
	prog := parseOK(t, `x := 5`)
	if len(prog.Stmts) != 1 {
		t.Fatalf("want 1 stmt, got %d", len(prog.Stmts))
	}
	def, ok := prog.Stmts[0].(*ast.DefineStmt)
	if !ok {
		t.Fatalf("want DefineStmt, got %T", prog.Stmts[0])
	}
	if def.Name != "x" {
		t.Errorf("want name x, got %s", def.Name)
	}
	if lit, ok := def.Val.(*ast.IntLit); !ok || lit.Val != 5 {
		t.Errorf("want IntLit(5), got %#v", def.Val)
	}
}

func TestFieldAssign(t *testing.T) {
	prog := parseOK(t, `job.ctl = "stop"`)
	as, ok := prog.Stmts[0].(*ast.AssignStmt)
	if !ok {
		t.Fatalf("want AssignStmt, got %T", prog.Stmts[0])
	}
	fa, ok := as.Target.(*ast.FieldAccess)
	if !ok {
		t.Fatalf("want FieldAccess target, got %T", as.Target)
	}
	if fa.Field != "ctl" {
		t.Errorf("want field ctl, got %s", fa.Field)
	}
	if sl, ok := as.Val.(*ast.StringLit); !ok || sl.Val != "stop" {
		t.Errorf("want StringLit(stop), got %#v", as.Val)
	}
}

func TestBinaryPrecedence(t *testing.T) {
	prog := parseOK(t, `1 + 2 * 3`)
	es := prog.Stmts[0].(*ast.ExprStmt)
	be, ok := es.X.(*ast.BinaryExpr)
	if !ok {
		t.Fatalf("want top-level BinaryExpr, got %T", es.X)
	}
	if _, ok := be.Left.(*ast.IntLit); !ok {
		t.Errorf("want left=IntLit(1), got %#v", be.Left)
	}
	rhs, ok := be.Right.(*ast.BinaryExpr)
	if !ok {
		t.Fatalf("want right=BinaryExpr(2*3), got %#v", be.Right)
	}
	if l := rhs.Left.(*ast.IntLit).Val; l != 2 {
		t.Errorf("want 2, got %d", l)
	}
}

func TestRecordLiteral(t *testing.T) {
	prog := parseOK(t, `{ name: "job1", pid: 42 }`)
	es := prog.Stmts[0].(*ast.ExprStmt)
	rec, ok := es.X.(*ast.RecordLit)
	if !ok {
		t.Fatalf("want RecordLit, got %T", es.X)
	}
	if len(rec.Fields) != 2 {
		t.Fatalf("want 2 fields, got %d", len(rec.Fields))
	}
	if rec.Fields[0].Name != "name" || rec.Fields[1].Name != "pid" {
		t.Errorf("unexpected field names: %#v", rec.Fields)
	}
}

func TestTableLiteral(t *testing.T) {
	prog := parseOK(t, `[{a: 1}, {a: 2}]`)
	es := prog.Stmts[0].(*ast.ExprStmt)
	tbl, ok := es.X.(*ast.TableLit)
	if !ok {
		t.Fatalf("want TableLit, got %T", es.X)
	}
	if len(tbl.Rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(tbl.Rows))
	}
}

func TestListLiteral(t *testing.T) {
	prog := parseOK(t, `[1, 2, 3]`)
	es := prog.Stmts[0].(*ast.ExprStmt)
	lst, ok := es.X.(*ast.ListLit)
	if !ok {
		t.Fatalf("want ListLit, got %T", es.X)
	}
	if len(lst.Elements) != 3 {
		t.Fatalf("want 3 elements, got %d", len(lst.Elements))
	}
}

func TestPipeWithClosureAndFieldAccess(t *testing.T) {
	prog := parseOK(t, `jobs | where { |j| j.status == "running" }`)
	es := prog.Stmts[0].(*ast.ExprStmt)
	pipe, ok := es.X.(*ast.PipeExpr)
	if !ok {
		t.Fatalf("want PipeExpr, got %T", es.X)
	}
	if _, ok := pipe.Left.(*ast.Ident); !ok {
		t.Errorf("want left=Ident(jobs), got %#v", pipe.Left)
	}
	call, ok := pipe.Right.(*ast.Call)
	if !ok {
		t.Fatalf("want right=Call, got %#v", pipe.Right)
	}
	if fn, ok := call.Fn.(*ast.Ident); !ok || fn.Name != "where" {
		t.Fatalf("want fn=where, got %#v", call.Fn)
	}
	if len(call.Args) != 1 {
		t.Fatalf("want 1 arg, got %d", len(call.Args))
	}
	clo, ok := call.Args[0].(*ast.Closure)
	if !ok {
		t.Fatalf("want Closure arg, got %#v", call.Args[0])
	}
	if len(clo.Params) != 1 || clo.Params[0].Name != "j" {
		t.Errorf("want params [j], got %v", clo.Params)
	}
	if len(clo.Body) != 1 {
		t.Fatalf("want 1 body stmt, got %d", len(clo.Body))
	}
	bodyExpr := clo.Body[0].(*ast.ExprStmt).X
	cmp, ok := bodyExpr.(*ast.BinaryExpr)
	if !ok {
		t.Fatalf("want BinaryExpr body, got %#v", bodyExpr)
	}
	fa, ok := cmp.Left.(*ast.FieldAccess)
	if !ok || fa.Field != "status" {
		t.Errorf("want left=field access .status, got %#v", cmp.Left)
	}
}

func TestChainedPipe(t *testing.T) {
	prog := parseOK(t, `jobs | where { |j| j.ok } | count()`)
	es := prog.Stmts[0].(*ast.ExprStmt)
	outer, ok := es.X.(*ast.PipeExpr)
	if !ok {
		t.Fatalf("want PipeExpr, got %T", es.X)
	}
	// left-associative: (jobs | where{...}) | count()
	if _, ok := outer.Left.(*ast.PipeExpr); !ok {
		t.Fatalf("want left to be a nested PipeExpr, got %#v", outer.Left)
	}
	call, ok := outer.Right.(*ast.Call)
	if !ok {
		t.Fatalf("want right=Call(count), got %#v", outer.Right)
	}
	if fn := call.Fn.(*ast.Ident); fn.Name != "count" {
		t.Errorf("want count, got %s", fn.Name)
	}
}

func TestExternalCall(t *testing.T) {
	prog := parseOK(t, `%grep "foo" file`)
	es := prog.Stmts[0].(*ast.ExprStmt)
	ext, ok := es.X.(*ast.ExternalCall)
	if !ok {
		t.Fatalf("want ExternalCall, got %T", es.X)
	}
	if ext.Name != "grep" {
		t.Errorf("want name grep, got %s", ext.Name)
	}
	if len(ext.Args) != 2 {
		t.Fatalf("want 2 args, got %d", len(ext.Args))
	}
}

func TestExternalCallInPipe(t *testing.T) {
	prog := parseOK(t, `jobs | %grep "foo"`)
	es := prog.Stmts[0].(*ast.ExprStmt)
	pipe := es.X.(*ast.PipeExpr)
	if _, ok := pipe.Right.(*ast.ExternalCall); !ok {
		t.Fatalf("want right=ExternalCall, got %#v", pipe.Right)
	}
}

func TestWhileExpr(t *testing.T) {
	prog := parseOK(t, `while i < 5 { i = i + 1 }`)
	es := prog.Stmts[0].(*ast.ExprStmt)
	we, ok := es.X.(*ast.WhileExpr)
	if !ok {
		t.Fatalf("want WhileExpr, got %T", es.X)
	}
	if _, ok := we.Cond.(*ast.BinaryExpr); !ok {
		t.Fatalf("want Cond=BinaryExpr, got %T", we.Cond)
	}
	if len(we.Body) != 1 {
		t.Fatalf("want 1 body stmt, got %d", len(we.Body))
	}
}

func TestBreakContinueExprs(t *testing.T) {
	prog := parseOK(t, `while true { break }`)
	we := prog.Stmts[0].(*ast.ExprStmt).X.(*ast.WhileExpr)
	bs := we.Body[0].(*ast.ExprStmt)
	if _, ok := bs.X.(*ast.BreakExpr); !ok {
		t.Fatalf("want BreakExpr, got %T", bs.X)
	}

	prog2 := parseOK(t, `while true { continue }`)
	we2 := prog2.Stmts[0].(*ast.ExprStmt).X.(*ast.WhileExpr)
	cs := we2.Body[0].(*ast.ExprStmt)
	if _, ok := cs.X.(*ast.ContinueExpr); !ok {
		t.Fatalf("want ContinueExpr, got %T", cs.X)
	}
}

func TestPassthroughStmt(t *testing.T) {
	prog := parseOK(t, `$vim "file.txt"`)
	pt, ok := prog.Stmts[0].(*ast.PassthroughStmt)
	if !ok {
		t.Fatalf("want PassthroughStmt, got %T", prog.Stmts[0])
	}
	if pt.Name != "vim" {
		t.Errorf("want name vim, got %s", pt.Name)
	}
	if len(pt.Args) != 1 {
		t.Fatalf("want 1 arg, got %d", len(pt.Args))
	}
}

// TestPassthroughRejectedInsidePipe locks in ast.PassthroughStmt's core
// design constraint: $cmd is never offered as a primary-expression
// option (unlike %cmd/ExternalCall), so it can't appear as a pipe's
// right-hand side or any other expression operand -- there's no
// captured value for it to produce, so composing it wouldn't make
// sense.
func TestPassthroughRejectedInsidePipe(t *testing.T) {
	p := New(`%echo "x" | $cat`)
	p.ParseProgram()
	if len(p.Errors()) == 0 {
		t.Fatal("$cmd used as a pipe's right-hand side should be a parse error")
	}
}

func TestPassthroughAmpersandRejected(t *testing.T) {
	p := New(`$vim "file.txt" &`)
	p.ParseProgram()
	if len(p.Errors()) == 0 {
		t.Fatal("'&' after $cmd should be a parse error -- there's no job to background")
	}
}

func TestPathLiteral(t *testing.T) {
	prog := parseOK(t, `p := /local/bin`)
	def := prog.Stmts[0].(*ast.DefineStmt)
	pl, ok := def.Val.(*ast.PathLit)
	if !ok {
		t.Fatalf("want PathLit, got %T", def.Val)
	}
	if pl.Val != "/local/bin" {
		t.Errorf("want /local/bin, got %s", pl.Val)
	}
}

func TestIfElse(t *testing.T) {
	prog := parseOK(t, `if x == 1 { y := 2 } else { y := 3 }`)
	es := prog.Stmts[0].(*ast.ExprStmt)
	ie, ok := es.X.(*ast.IfExpr)
	if !ok {
		t.Fatalf("want IfExpr, got %T", es.X)
	}
	if len(ie.Then) != 1 || len(ie.Else) != 1 {
		t.Fatalf("want 1 stmt in each branch, got then=%d else=%d", len(ie.Then), len(ie.Else))
	}
}

func TestErrCheckPostfix(t *testing.T) {
	prog := parseOK(t, `risky()?`)
	es := prog.Stmts[0].(*ast.ExprStmt)
	if _, ok := es.X.(*ast.ErrCheck); !ok {
		t.Fatalf("want ErrCheck, got %T", es.X)
	}
}

func TestMultiStatementNewlineSeparated(t *testing.T) {
	prog := parseOK(t, "x := 1\ny := x + 1\ny")
	if len(prog.Stmts) != 3 {
		t.Fatalf("want 3 stmts, got %d: %#v", len(prog.Stmts), prog.Stmts)
	}
}

func TestDurationLiteral(t *testing.T) {
	prog := parseOK(t, `500ms`)
	es := prog.Stmts[0].(*ast.ExprStmt)
	d, ok := es.X.(*ast.DurationLit)
	if !ok {
		t.Fatalf("want DurationLit, got %T", es.X)
	}
	if d.Nanos != 500_000_000 {
		t.Errorf("want 500ms in nanos, got %d", d.Nanos)
	}
}

func TestBindStmtDefaultsToReplace(t *testing.T) {
	prog := parseOK(t, `bind /local/bin, /bin`)
	bs, ok := prog.Stmts[0].(*ast.BindStmt)
	if !ok {
		t.Fatalf("want BindStmt, got %T", prog.Stmts[0])
	}
	if bs.Disposition != "replace" {
		t.Errorf("want default disposition replace, got %s", bs.Disposition)
	}
	src, ok := bs.Src.(*ast.PathLit)
	if !ok || src.Val != "/local/bin" {
		t.Fatalf("want src=/local/bin, got %#v", bs.Src)
	}
	dst, ok := bs.Dst.(*ast.PathLit)
	if !ok || dst.Val != "/bin" {
		t.Fatalf("want dst=/bin, got %#v", bs.Dst)
	}
}

func TestBindStmtExplicitDisposition(t *testing.T) {
	prog := parseOK(t, `bind /local/bin, /bin, before`)
	bs := prog.Stmts[0].(*ast.BindStmt)
	if bs.Disposition != "before" {
		t.Errorf("want before, got %s", bs.Disposition)
	}
}

func TestBindStmtNamespaceUnionSource(t *testing.T) {
	prog := parseOK(t, `bind a + b, /dst, after`)
	bs := prog.Stmts[0].(*ast.BindStmt)
	if bs.Disposition != "after" {
		t.Errorf("want after, got %s", bs.Disposition)
	}
	be, ok := bs.Src.(*ast.BinaryExpr)
	if !ok {
		t.Fatalf("want union src to parse as a BinaryExpr(+), got %#v", bs.Src)
	}
	if be.Op != token.PLUS {
		t.Errorf("want '+', got %s", be.Op)
	}
	dst, ok := bs.Dst.(*ast.PathLit)
	if !ok || dst.Val != "/dst" {
		t.Fatalf("want dst=/dst, got %#v", bs.Dst)
	}
}

func TestBindStmtInsideBlock(t *testing.T) {
	prog := parseOK(t, `if true { bind /a, /b }`)
	ie := prog.Stmts[0].(*ast.ExprStmt).X.(*ast.IfExpr)
	if len(ie.Then) != 1 {
		t.Fatalf("want 1 stmt, got %d", len(ie.Then))
	}
	if _, ok := ie.Then[0].(*ast.BindStmt); !ok {
		t.Fatalf("want BindStmt inside block, got %T", ie.Then[0])
	}
}

func TestUnbindStmt(t *testing.T) {
	prog := parseOK(t, `unbind /local`)
	us, ok := prog.Stmts[0].(*ast.UnbindStmt)
	if !ok {
		t.Fatalf("want UnbindStmt, got %T", prog.Stmts[0])
	}
	dst, ok := us.Dst.(*ast.PathLit)
	if !ok || dst.Val != "/local" {
		t.Fatalf("want dst=/local, got %#v", us.Dst)
	}
}

func TestClosureDefaultParams(t *testing.T) {
	prog := parseOK(t, `{ |a, b = 10| a + b }`)
	clo := prog.Stmts[0].(*ast.ExprStmt).X.(*ast.Closure)
	if len(clo.Params) != 2 {
		t.Fatalf("want 2 params, got %d", len(clo.Params))
	}
	if clo.Params[0].Name != "a" || clo.Params[0].Default != nil {
		t.Errorf("want a with no default, got %#v", clo.Params[0])
	}
	if clo.Params[1].Name != "b" || clo.Params[1].Default == nil {
		t.Errorf("want b with a default, got %#v", clo.Params[1])
	}
	lit, ok := clo.Params[1].Default.(*ast.IntLit)
	if !ok || lit.Val != 10 {
		t.Fatalf("want b's default = 10, got %#v", clo.Params[1].Default)
	}
}

func TestClosureDefaultMustTrailIsParseError(t *testing.T) {
	p := New(`{ |a = 1, b| a + b }`)
	p.ParseProgram()
	if len(p.Errors()) == 0 {
		t.Fatal("a required param after a defaulted one should be a parse error")
	}
}

func TestBackgroundExternalCall(t *testing.T) {
	prog := parseOK(t, `j := %sleep 30 &`)
	def := prog.Stmts[0].(*ast.DefineStmt)
	bg, ok := def.Val.(*ast.Background)
	if !ok {
		t.Fatalf("want Background, got %T", def.Val)
	}
	if bg.Call.Name != "sleep" {
		t.Errorf("want command sleep, got %s", bg.Call.Name)
	}
	if len(bg.Call.Args) != 1 {
		t.Fatalf("want 1 arg, got %d", len(bg.Call.Args))
	}
}

func TestBackgroundBareStatement(t *testing.T) {
	prog := parseOK(t, `%sleep 30 &`)
	es := prog.Stmts[0].(*ast.ExprStmt)
	if _, ok := es.X.(*ast.Background); !ok {
		t.Fatalf("want Background, got %T", es.X)
	}
}

func TestBackgroundRejectsNonExternalCall(t *testing.T) {
	p := New(`j := 5 &`)
	p.ParseProgram()
	if len(p.Errors()) == 0 {
		t.Fatal("backgrounding a non-external-call expression should be a parse error")
	}
}
