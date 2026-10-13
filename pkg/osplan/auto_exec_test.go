package osplan

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The tests that run the blocks of the owner's runbook (runbook_exec_test.go, runbook_states_test.go) run a second time on the text of the unit. They are the same tests, written
// once: suiteAuto says which text realRunbook and newShellBox give them, TestTheExecSuitesRunOnTheAutoText sets it, and what differs in the unit's text is said where the test
// looks at it (the words that the owner types, the terminal, the pty of apt, Docker's answer). A test that is about the owner's gates is in manualOnly, with its reason, and a
// test that is in neither list fails TestEveryExecTestIsInTheAutoSetOrManualOnly: a new test is a choice, made in the open.

// suiteAuto says that the tests that run blocks run the text of the unit. Only TestTheExecSuitesRunOnTheAutoText sets it, and the tests of a package do not run at the same time.
var suiteAuto bool

// buildFor is the text of a plan as the suite that is running has it: the owner's runbook, or the unit's blocks in the same frame (the markers that blockOf looks for), with the
// hostname of the box of the tests, the run of the box, and the file of cgroups that the box has.
func buildFor(t *testing.T, p Plan) *Runbook {
	t.Helper()
	if !suiteAuto {
		rb, err := BuildRunbook(p)
		if err != nil {
			t.Fatal(err)
		}
		return rb
	}
	rb, _ := autoFrame(t, p, AutoParams{RunID: testRunID, Hostname: testHostname, CgroupFile: "./cgroup"})
	return rb
}

// autoFrame is the nine blocks of the unit's text in the frame of the owner's runbook, so that blockOf finds them, and the text itself (for block F, which has no place in the frame).
func autoFrame(t *testing.T, p Plan, a AutoParams) (*Runbook, *AutoRunbook) {
	t.Helper()
	manual, err := BuildRunbook(p)
	if err != nil {
		t.Fatal(err)
	}
	ar := autoOf(t, p, a)
	var b strings.Builder
	for i, blk := range ar.Blocks {
		fmt.Fprintf(&b, "%s%d %s\n%s%s%d\n", blockStart, i, blk.ID, blk.Text, blockEnd, i)
	}
	return &Runbook{PlanID: manual.PlanID, Hop: manual.Hop, Text: b.String(), Blocks: manual.Blocks}, ar
}

// aptqOpts are the options that the aptq of the text that the suite runs gives apt: the unit's text has no pty for apt to take over.
func aptqOpts() string {
	pty := ""
	if suiteAuto {
		pty = "-o Dpkg::Use-Pty=0 "
	}
	return "-y -q " + pty + "-o DPkg::Lock::Timeout=300 -o Acquire::Retries=3 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold"
}

// autoSuite are the tests that run on both texts.
var autoSuite = []func(*testing.T){
	TestBlock0ChecksTheMachineBeforeAnythingElse,
	TestBlock0SaysThatItDoesNotSeeTheLocksOfApt,
	TestBlock0TakesDpkgForAPackageManagerOnlyWhenItChangesPackages,
	TestABlockDoesNotRunWhenTheOneBeforeItDidNotEnd,
	TestBlock0MakesTheBackupDirectoryPrivateWhateverTheUmaskAndWhateverChmodDoes,
	TestEveryBlockMakesTheBackupDirectoryPrivateBeforeItWritesInIt,
	TestBlock2WritesTheStateInAPrivateDirectoryAndTheArchiveOfEtcIsPrivateToo,
	TestBlock2RunsDockerInspectOnlyWhenThereAreContainers,
	TestBlock2StopsOnceTheUpgradeHasWentOnAndTakesItBackAfterTheRestore,
	TestBlock2SaysWhatTarSaidAndCopiesWhatItShould,
	TestBlock1StopsTheTimersThatRunAndRemembersWhichOnes,
	TestBlock6GoesOnUntilNothingIsLeftAndStopsWhenSomethingIs,
	TestBlock5NeedsTheConfirmationAndStopsAfterTheMinimalUpgradeIfAptSaysSo,
	TestBlock5DoesNotTakeATextItDoesNotKnowForNothingToRemove,
	TestBlock7ChecksTheKernelTheInitrdAndSshdBeforeTheRestart,
	TestBlock7SeesARestartThatCameBeforeItEnded,
	TestBlock7SaysWhenThereIsNoGrubConfigToLookAt,
	TestAPlanWhoseListOfNewPackagesWasCutExpectsANewKernel,
	TestBlock8PutsDockersSourceBackWithTheModeItHad,
	TestBlock8NeedsARestartAndDebian12,
	TestBlock3WritesTheTreeAndStopsOnItsOwnErrors,
	TestBlock3ReadsWhatItWroteBeforeItMovesAnything,
	TestBlock3MakesAptOrigWholeOrNotAtAll,
	TestBlock3WritesTheFilesOfEtcAptWithTheModeTheyHad,
	TestBlock3ReadsTheSourcesBackAfterTheMoves,
	TestBlock3StopsTheTimersAgain,
	TestTheAptCommandsOfTheBlocksRunWithoutAQuestionAndWithTheOptionsOfTheHopButItsPty,
	TestBlock4DownloadsAndStopsWhenNothingWasDownloaded,
	TestTheShellReadsTheNamesOfSourceFilesAsAptAndTheCodeDo,
	TestBlock0LooksAtWhatTheHopWouldBreakOnAfterThePointOfNoReturn,
	TestBlock0WarnsOfModulesBuiltFromSourceAndGoesOn,
	TestBlock0AsksAptGetCheckWithTheWaitForTheLockOfDpkg,
	TestNoBlockRunsOnceTheHopHasEnded,
	TestNoBlockRunsOnAMachineThatAnotherPlanTookToItsEnd,
	TestABlockStopsOnAMachineThatIsNotDebian11UnlessItsPlanHasBegunTheUpgrade,
	TestBlock6SaysThatAnInterruptedDpkgOrAnUnmetDependencyIsNeitherTheLockNorTheNetwork,
	TestBlock3PastedAgainOnTheSourcesItWroteSendsTheOwnerToTheRestoreScript,
	TestBlocks3And5And6StopWhileAPackageManagerIsAtWork,
	TestBlock5TakesItsMarkerBackWhenAptFailedBeforeItChangedAnything,
	TestTheStopsAfterThePointOfNoReturnNameTheLogs,
	TestBlock8StartsTheTimersThatWereRunningAndRemovesTheirState,
	TestBlock2WritesDownTheUnitsThatFailAndTheReCasaOSUnitsAndBlock8ComparesThem,
	TestBlock8ListsTheFilesThatDpkgLeftSinceBlock2AndLeavesTheCommentsOfDockersSourceAlone,
	TestBlock1PutsTheStateOfTheTimersInPlaceAllAtOnceAndAdoptsTheOneThatAnotherRunPut,
	TestBlock1KeepsItsStateWhenLnFailsForAnotherReasonThanTheNameBeingTaken,
	TestTwoRunsOfBlock1AtTheSameTimeKeepOneWholeState,
	TestBlocks0And3SendTheOwnerToTheRestoreScriptOfAnotherPlanWhoseBlock3Ran,
	TestBlock0SaysWhatItKnowsOfItsOwnFolderAndNoMore,
	TestBlocks0And6And8StopWhenDpkgAuditSaysSomethingOnItsStderrOrEndsWithAnError,
	TestTheRepairLadderOfBlock6SimulatesTheRepairAndTheFullUpgradeAfterItAndGuardsEachOne,
	TestBlock4StopsOnceTheUpgradeHasBegunAndSaysWhereToGoByTheMarker,
	TestTheBusyStopOfBlock5ChoosesItsWordsByTheMarker,
	TestTheEndsOfBlocks6And8SayWhatComesNextAndNotToAutoremoveBlindly,
	TestEveryRoomStopOfBlock0SaysWhatToDo,
	TestBlock3SendsToANewPlanOnlyWhenTheMachineIsNotWhatThePlanSaw,
}

// manualOnly are the tests that stay on the owner's text, and why. Each is about a gate that only a person at a terminal has, or about the words and the header that only the
// owner's text has. What the unit has in their place is tested where the reason says.
var manualOnly = map[string]string{
	"TestBlock0AsksForAConsoleOrTmuxOrScreenWhileTheMachineIsStillDebian11":   "the terminal that survives a dropped connection: a unit has no terminal, and the gate of the unit takes its place (hook H5, TestTheHooksEachHaveTheirEffectInTheAutoText)",
	"TestBlocks5And6RunOnlyWhereADroppedConnectionDoesNotKillApt":             "the terminal that survives a dropped connection (tmux, screen, a console): a unit has none (hook H5)",
	"TestBlocks5And6DoNotTellToInstallTmuxWhereTheSourcesAreDebian12":         "the words that tell a person at a terminal to install tmux or go to the console (hook H5)",
	"TestTheRestoreScriptSaysHowToStartTheTimersThatBlock1Stopped":            "runs the restore script alone, which is the same bytes in both texts (TestAutoBlock3HoldsTheRestoreScriptAndTheTreeByteForByte)",
	"TestTheTextClaimsNoMoreThanItChecksAndKeepsTheStateOfTheTimersInOneFile": "reads the header and the prose of the runbook, which only the owner's text has (TestTheAutoTextSaysNothingOfHowItCameToBeAndRunsNothingThatRestarts)",
	"TestAptRunsWithItsPtySoThatTermLogHoldsWhatThePackagesPrint":             "the owner's text keeps apt's pty so that term.log holds what the scripts print; the unit's turns it off, as the measured hop did (hook H6)",
}

// execSuiteFiles are the files whose tests are in one of the two lists.
var execSuiteFiles = []string{"runbook_exec_test.go", "runbook_states_test.go"}

func TestEveryExecTestIsInTheAutoSetOrManualOnly(t *testing.T) {
	inAuto := map[string]bool{}
	for _, fn := range autoSuite {
		name := funcName(fn)
		if inAuto[name] {
			t.Errorf("%s is twice in autoSuite", name)
		}
		inAuto[name] = true
		if _, both := manualOnly[name]; both {
			t.Errorf("%s is in autoSuite and in manualOnly", name)
		}
	}
	seen := map[string]bool{}
	for _, file := range execSuiteFiles {
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !strings.HasPrefix(fd.Name.Name, "Test") {
				continue
			}
			name := fd.Name.Name
			seen[name] = true
			_, manual := manualOnly[name]
			if !inAuto[name] && !manual {
				t.Errorf("%s (%s) is neither in autoSuite nor in manualOnly: say whether it runs on the unit's text too, or why it does not", name, file)
			}
		}
	}
	for name := range inAuto {
		if !seen[name] {
			t.Errorf("autoSuite has %s, which is no test of %v", name, execSuiteFiles)
		}
	}
	for name, why := range manualOnly {
		if !seen[name] {
			t.Errorf("manualOnly has %s, which is no test of %v", name, execSuiteFiles)
		}
		if len(why) < 20 {
			t.Errorf("manualOnly has no reason for %s", name)
		}
	}
}

func funcName(fn func(*testing.T)) string {
	full := runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()
	return full[strings.LastIndex(full, ".")+1:]
}

// TestTheExecSuitesRunOnTheAutoText runs every test of autoSuite with the text of the unit in the place of the owner's.
func TestTheExecSuitesRunOnTheAutoText(t *testing.T) {
	for _, fn := range autoSuite {
		name := funcName(fn)
		t.Run(name, func(t *testing.T) {
			if suiteAuto {
				t.Fatal("the suite is running inside itself")
			}
			suiteAuto = true
			defer func() { suiteAuto = false }()
			fn(t)
		})
	}
}

var _ = slices.Contains[[]string]
