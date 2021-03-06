package algebrain

import (
	"fmt"
	"math"
	"math/rand"
	"strconv"

	"github.com/unixpickle/algebrain/mathexpr"
	"github.com/unixpickle/anyvec"
	"github.com/unixpickle/anyvec/anyvec32"
	"github.com/unixpickle/num-analysis/linalg"
)

// A Sample contains a query (e.g. "factorize x^2+x") and
// the expected result (e.g. "x(x+1)").
type Sample struct {
	Query    string
	Response string
}

// InputSequence generates the sample's input sequence.
func (s *Sample) InputSequence() []anyvec.Vector {
	res := make([]anyvec.Vector, len(s.Query))
	for i, x := range s.Query {
		res[i] = oneHotVector(x)
	}
	return res
}

// DecoderOutSequence is the desired output from the
// decoder if all goes well.
func (s *Sample) DecoderOutSequence() []anyvec.Vector {
	res := make([]anyvec.Vector, len(s.Response)+1)
	for i, x := range s.Response {
		res[i] = oneHotVector(x)
	}
	res[len(res)-1] = oneHotVector(Terminator)
	return res
}

// DecoderInSequence generates the desired input to be fed
// to the decoder if all goes well.
func (s *Sample) DecoderInSequence() []anyvec.Vector {
	res := make([]anyvec.Vector, len(s.Response)+1)
	res[0] = oneHotVector(0)
	for i, x := range s.Response {
		res[i+1] = oneHotVector(x)
	}
	return res
}

// A Generator generates random Samples from a template.
type Generator interface {
	Generate() *Sample
}

// A ShiftGenerator generates Samples with queries like
// "shift x by 2 in x^2+2", producing results like
// "(x-2)^2+2".
type ShiftGenerator struct {
	Generator *mathexpr.Generator
	MaxDepth  int
}

// Generate generates a graph shifting sample.
func (s *ShiftGenerator) Generate() *Sample {
	expr := s.Generator.Generate(s.MaxDepth)
	shiftVar := s.Generator.VarNames[rand.Intn(len(s.Generator.VarNames))]
	num := generateNumber(*s.Generator)
	query := fmt.Sprintf("shift %s by %s in %s", shiftVar, num, expr)
	output := s.shiftNode(shiftVar, num, expr).String()
	return &Sample{
		Query:    query,
		Response: output,
	}
}

func (s *ShiftGenerator) shiftNode(varName string, amount mathexpr.Node,
	n mathexpr.Node) mathexpr.Node {
	if n, ok := n.(mathexpr.RawNode); ok {
		if string(n) == varName {
			return &mathexpr.BinaryOp{
				Op:    mathexpr.SubtractOp,
				Left:  n,
				Right: amount,
			}
		}
	}
	for i, x := range n.Children() {
		n.SetChild(i, s.shiftNode(varName, amount, x))
	}
	return n
}

// A ScaleGenerator generates Samples with queries like
// "scale x by 2 in x^2", expecting "(2*x)^2".
type ScaleGenerator struct {
	Generator *mathexpr.Generator
	MaxDepth  int
}

func (s *ScaleGenerator) Generate() *Sample {
	expr := s.Generator.Generate(s.MaxDepth)
	shiftVar := s.Generator.VarNames[rand.Intn(len(s.Generator.VarNames))]
	num := generateNumber(*s.Generator)
	query := fmt.Sprintf("scale %s by %s in %s", shiftVar, num, expr)
	output := s.scaleNode(shiftVar, num, expr).String()
	return &Sample{
		Query:    query,
		Response: output,
	}
}

func (s *ScaleGenerator) scaleNode(varName string, amount mathexpr.Node,
	n mathexpr.Node) mathexpr.Node {
	if n, ok := n.(mathexpr.RawNode); ok {
		if string(n) == varName {
			return &mathexpr.BinaryOp{
				Op:    mathexpr.MultiplyOp,
				Left:  n,
				Right: amount,
			}
		}
	}
	for i, x := range n.Children() {
		n.SetChild(i, s.scaleNode(varName, amount, x))
	}
	return n
}

// An EvalGenerator generates expressions with no
// variables which evaluate down to a single number.
type EvalGenerator struct {
	Generator *mathexpr.Generator
	MaxDepth  int
	AllInts   bool

	UseDiv bool
	UsePow bool
}

func (e *EvalGenerator) Generate() *Sample {
	var expr mathexpr.Node
	for {
		expr = e.Generator.Generate(e.MaxDepth)
		if e.valid(expr) {
			break
		}
	}
	val := e.evaluateExpr(expr)
	prec := 2
	if e.AllInts {
		prec = 0
	}
	outStr := strconv.FormatFloat(val, 'f', prec, 64)
	return &Sample{
		Query:    "evaluate " + expr.String(),
		Response: "Result: " + outStr,
	}
}

func (e *EvalGenerator) evaluateExpr(n mathexpr.Node) float64 {
	switch n := n.(type) {
	case *mathexpr.BinaryOp:
		left := e.evaluateExpr(n.Left)
		right := e.evaluateExpr(n.Right)
		switch n.Op {
		case mathexpr.AddOp:
			return left + right
		case mathexpr.SubtractOp:
			return left - right
		case mathexpr.MultiplyOp:
			return left * right
		case mathexpr.DivideOp:
			if e.AllInts {
				return float64(int(left) / int(right))
			} else {
				return left / right
			}
		case mathexpr.PowOp:
			if e.AllInts && right < 0 {
				return 0
			}
			return math.Pow(left, right)
		}
	case *mathexpr.NegOp:
		return -e.evaluateExpr(n.Node)
	case mathexpr.RawNode:
		res, _ := strconv.ParseFloat(string(n), 64)
		return res
	}
	panic("unsupported expression: " + n.String())
}

func (e *EvalGenerator) valid(n mathexpr.Node) bool {
	for _, child := range n.Children() {
		if !e.valid(child) {
			return false
		}
	}
	switch n := n.(type) {
	case *mathexpr.BinaryOp:
		right := e.evaluateExpr(n.Right)
		if (right == 0 || !e.UseDiv) && n.Op == mathexpr.DivideOp {
			return false
		}
		if !e.UsePow && n.Op == mathexpr.PowOp {
			return false
		}
	}
	return true
}

func generateNumber(g mathexpr.Generator) mathexpr.RawNode {
	g.VarNames = nil
	g.ConstNames = nil
	return g.Generate(0).(mathexpr.RawNode)
}

func zeroVector() linalg.Vector {
	return make(linalg.Vector, CharCount)
}

func oneHotVector(x rune) anyvec.Vector {
	ix := int(x)
	if ix >= CharCount || ix < 0 {
		panic("rune out of range: " + string(x))
	}
	data := make([]float32, CharCount)
	data[ix] = 1
	return anyvec32.MakeVectorData(data)
}
