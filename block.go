package algebrain

import (
	"io/ioutil"

	"github.com/unixpickle/autofunc"
	"github.com/unixpickle/neuralstruct"
	"github.com/unixpickle/num-analysis/linalg"
	"github.com/unixpickle/serializer"
	"github.com/unixpickle/sgd"
	"github.com/unixpickle/weakai/neuralnet"
	"github.com/unixpickle/weakai/rnn"
)

const (
	CharCount  = 128
	Terminator = 0

	maxResponseLen = 1000
)

func init() {
	var b Block
	serializer.RegisterTypedDeserializer(b.SerializerType(), DeserializeBlock)
}

type blockState struct {
	Reading bool
	State   rnn.State
}

type blockRState struct {
	Reading bool
	State   rnn.RState
}

// A Block uses two RNNs to evaluate an algebra query.
type Block struct {
	Reader rnn.Block
	Writer rnn.Block
}

// DeserializeBlock deserializes a block.
func DeserializeBlock(d []byte) (*Block, error) {
	var res Block
	if err := serializer.DeserializeAny(d, &res.Reader, &res.Writer); err != nil {
		return nil, err
	}
	return &res, nil
}

// NewBlock creates a block that uses a multi-layer LSTM
// and an assortment of differentiable memory structures.
func NewBlock(dropout float64, structure neuralstruct.RAggregate, hiddenSizes ...int) *Block {
	resBlocks := make([]rnn.Block, 2)
	for i := range resBlocks {
		var sb rnn.StackedBlock
		inCount := structure.DataSize() + CharCount
		for _, hidden := range hiddenSizes {
			sb = append(sb, rnn.NewLSTM(inCount, hidden))
			sb = append(sb, rnn.NewNetworkBlock(neuralnet.Network{
				&neuralnet.DropoutLayer{
					KeepProbability: dropout,
				},
			}, 0))
			inCount = hidden
		}

		outCount := CharCount + structure.ControlSize()
		outNet := neuralnet.Network{
			&neuralnet.DenseLayer{
				InputCount:  inCount,
				OutputCount: outCount,
			},
			&neuralstruct.PartialActivation{
				Activations: []neuralnet.Layer{
					structure.SuggestedActivation(),
					&neuralnet.LogSoftmaxLayer{},
				},
				Ranges: []neuralstruct.ComponentRange{
					{Start: 0, End: structure.ControlSize()},
					{Start: structure.ControlSize(), End: outCount},
				},
			},
		}
		outNet.Randomize()
		sb = append(sb, rnn.NewNetworkBlock(outNet, 0))

		resBlocks[i] = &neuralstruct.Block{
			Struct: structure,
			Block:  sb,
		}
	}
	return &Block{Reader: resBlocks[0], Writer: resBlocks[1]}
}

// LoadBlock loads a block from a file.
func LoadBlock(path string) (*Block, error) {
	contents, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return DeserializeBlock(contents)
}

// StartState returns a state which wraps the reader's
// start state.
func (b *Block) StartState() rnn.State {
	return &blockState{
		Reading: true,
		State:   b.Reader.StartState(),
	}
}

// StartRState is like StartState.
func (b *Block) StartRState(rv autofunc.RVector) rnn.RState {
	return &blockRState{
		Reading: true,
		State:   b.Reader.StartRState(rv),
	}
}

// PropagateStart propagates through the start state.
func (b *Block) PropagateStart(s []rnn.State, u []rnn.StateGrad, g autofunc.Gradient) {
	internalS := make([]rnn.State, len(s))
	for i, x := range s {
		internalS[i] = x.(*blockState).State
	}
	b.Reader.PropagateStart(internalS, u, g)
}

// PropagateStartR propagates through the start state.
func (b *Block) PropagateStartR(s []rnn.RState, u []rnn.RStateGrad, rg autofunc.RGradient,
	g autofunc.Gradient) {
	internalS := make([]rnn.RState, len(s))
	for i, x := range s {
		internalS[i] = x.(*blockRState).State
	}
	b.Reader.PropagateStartR(internalS, u, rg, g)
}

// ApplyBlock applies the block to an input.
//
// An input with the Terminator component set to non-zero
// signals that the remaining timesteps for that sequence
// should be fed into the writer.
func (b *Block) ApplyBlock(s []rnn.State, in []autofunc.Result) rnn.BlockResult {
	reading := make([]bool, len(s))
	internalS := make([]rnn.State, len(s))
	for i, x := range s {
		reading[i] = x.(*blockState).Reading
		internalS[i] = x.(*blockState).State
	}

	var readS, writeS []rnn.State
	splitReadWrite(reading, internalS, &readS, &writeS)
	var readIn, writeIn []autofunc.Result
	splitReadWrite(reading, in, &readIn, &writeIn)

	var readRes, writeRes rnn.BlockResult
	readRes = emptyResult{}
	writeRes = emptyResult{}
	if len(readIn) > 0 {
		readRes = b.Reader.ApplyBlock(readS, readIn)
	}
	if len(writeIn) > 0 {
		writeRes = b.Writer.ApplyBlock(writeS, writeIn)
	}

	res := &blockResult{
		Reading:  reading,
		ReadRes:  readRes,
		WriteRes: writeRes,
	}

	joinReadWrite(reading, readRes.Outputs(), writeRes.Outputs(), &res.OutVecs)

	var internalStates []rnn.State
	joinReadWrite(reading, readRes.States(), writeRes.States(), &internalStates)
	for i, x := range s {
		reading := x.(*blockState).Reading && in[i].Output()[Terminator] == 0
		res.OutStates = append(res.OutStates, &blockState{
			Reading: reading,
			State:   internalStates[i],
		})
	}

	return res
}

// ApplyBlockR is like ApplyBlock.
func (b *Block) ApplyBlockR(rv autofunc.RVector, s []rnn.RState,
	in []autofunc.RResult) rnn.BlockRResult {
	reading := make([]bool, len(s))
	internalS := make([]rnn.RState, len(s))
	for i, x := range s {
		reading[i] = x.(*blockRState).Reading
		internalS[i] = x.(*blockRState).State
	}

	var readS, writeS []rnn.RState
	splitReadWrite(reading, internalS, &readS, &writeS)
	var readIn, writeIn []autofunc.RResult
	splitReadWrite(reading, in, &readIn, &writeIn)

	var readRes, writeRes rnn.BlockRResult
	readRes = emptyResult{}
	writeRes = emptyResult{}
	if len(readIn) > 0 {
		readRes = b.Reader.ApplyBlockR(rv, readS, readIn)
	}
	if len(writeIn) > 0 {
		writeRes = b.Writer.ApplyBlockR(rv, writeS, writeIn)
	}

	res := &blockRResult{
		Reading:  reading,
		ReadRes:  readRes,
		WriteRes: writeRes,
	}

	joinReadWrite(reading, readRes.Outputs(), writeRes.Outputs(), &res.OutVecs)
	joinReadWrite(reading, readRes.ROutputs(), writeRes.ROutputs(), &res.ROutVecs)

	var internalStates []rnn.RState
	joinReadWrite(reading, readRes.RStates(), writeRes.RStates(), &internalStates)
	for i, x := range s {
		reading := x.(*blockRState).Reading && in[i].Output()[Terminator] == 0
		res.OutStates = append(res.OutStates, &blockRState{
			Reading: reading,
			State:   internalStates[i],
		})
	}

	return res
}

// Parameters gets the parameters of the block.
func (b *Block) Parameters() []*autofunc.Variable {
	var res []*autofunc.Variable
	for _, block := range []rnn.Block{b.Reader, b.Writer} {
		if l, ok := block.(sgd.Learner); ok {
			res = append(res, l.Parameters()...)
		}
	}
	return res
}

// SerializerType returns the unique ID used to serialize
// a Block with the serializer package.
func (b *Block) SerializerType() string {
	return "github.com/unixpickle/algebrain.Block"
}

// Serialize attempts to serialize the block.
func (b *Block) Serialize() ([]byte, error) {
	return serializer.SerializeAny(b.Reader, b.Writer)
}

// Dropout searches from dropout layers and toggles
// dropout on them.
func (b *Block) Dropout(enabled bool) {
	for _, block := range []rnn.Block{b.Reader, b.Writer} {
		structBlock, ok := block.(*neuralstruct.Block)
		if !ok {
			continue
		}
		sb, ok := structBlock.Block.(rnn.StackedBlock)
		if !ok {
			continue
		}
		for _, x := range sb {
			if n, ok := x.(*rnn.NetworkBlock); ok {
				for _, l := range n.Network() {
					if do, ok := l.(*neuralnet.DropoutLayer); ok {
						do.Training = enabled
					}
				}
			}
		}
	}
}

// Save writes the block to a file.
func (b *Block) Save(path string) error {
	enc, err := b.Serialize()
	if err != nil {
		return err
	}
	return ioutil.WriteFile(path, enc, 0755)
}

// Query runs a query against this block and returns the
// resulting string.
func (b *Block) Query(q string) string {
	r := &rnn.Runner{Block: b}
	for _, x := range q {
		if x < 0 || x > 128 {
			continue
		}
		r.StepTime(oneHotVector(x))
	}
	r.StepTime(oneHotVector(Terminator))

	var lastOut rune = Terminator
	var res string
	for {
		nextVec := r.StepTime(oneHotVector(lastOut))
		_, nextIdx := nextVec.Max()
		lastOut = rune(nextIdx)
		if lastOut == 0 || len(res) >= maxResponseLen {
			break
		}
		res += string(lastOut)
	}
	return res
}

type blockResult struct {
	Reading  []bool
	ReadRes  rnn.BlockResult
	WriteRes rnn.BlockResult

	OutVecs   []linalg.Vector
	OutStates []rnn.State
}

func (b *blockResult) Outputs() []linalg.Vector {
	return b.OutVecs
}

func (b *blockResult) States() []rnn.State {
	return b.OutStates
}

func (b *blockResult) PropagateGradient(u []linalg.Vector, s []rnn.StateGrad,
	g autofunc.Gradient) []rnn.StateGrad {
	var readU, writeU []linalg.Vector
	splitReadWrite(b.Reading, u, &readU, &writeU)
	var readS, writeS []rnn.StateGrad
	splitReadWrite(b.Reading, s, &readS, &writeS)

	readDown := b.ReadRes.PropagateGradient(readU, readS, g)
	writeDown := b.WriteRes.PropagateGradient(writeU, writeS, g)

	var down []rnn.StateGrad
	joinReadWrite(b.Reading, readDown, writeDown, &down)
	return down
}

type blockRResult struct {
	Reading  []bool
	ReadRes  rnn.BlockRResult
	WriteRes rnn.BlockRResult

	OutVecs   []linalg.Vector
	ROutVecs  []linalg.Vector
	OutStates []rnn.RState
}

func (b *blockRResult) Outputs() []linalg.Vector {
	return b.OutVecs
}

func (b *blockRResult) ROutputs() []linalg.Vector {
	return b.ROutVecs
}

func (b *blockRResult) RStates() []rnn.RState {
	return b.OutStates
}

func (b *blockRResult) PropagateRGradient(u, uR []linalg.Vector, s []rnn.RStateGrad,
	rg autofunc.RGradient, g autofunc.Gradient) []rnn.RStateGrad {
	var readU, writeU []linalg.Vector
	splitReadWrite(b.Reading, u, &readU, &writeU)
	var readUR, writeUR []linalg.Vector
	splitReadWrite(b.Reading, uR, &readUR, &writeUR)
	var readS, writeS []rnn.RStateGrad
	splitReadWrite(b.Reading, s, &readS, &writeS)

	readDown := b.ReadRes.PropagateRGradient(readU, readUR, readS, rg, g)
	writeDown := b.WriteRes.PropagateRGradient(writeU, writeUR, writeS, rg, g)

	var down []rnn.RStateGrad
	joinReadWrite(b.Reading, readDown, writeDown, &down)
	return down
}

type emptyResult struct{}

func (_ emptyResult) Outputs() []linalg.Vector {
	return nil
}

func (_ emptyResult) ROutputs() []linalg.Vector {
	return nil
}

func (_ emptyResult) States() []rnn.State {
	return nil
}

func (_ emptyResult) RStates() []rnn.RState {
	return nil
}

func (_ emptyResult) PropagateGradient(u []linalg.Vector, s []rnn.StateGrad,
	g autofunc.Gradient) []rnn.StateGrad {
	return nil
}

func (_ emptyResult) PropagateRGradient(u, uR []linalg.Vector, s []rnn.RStateGrad,
	rg autofunc.RGradient, g autofunc.Gradient) []rnn.RStateGrad {
	return nil
}
