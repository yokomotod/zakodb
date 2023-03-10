package transactonstest

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

type Query struct {
	Query   string
	Want    []sql.NullInt64
	WantErr string
}

func getGoID() int {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// fmt.Printf("%s\n", buf)
	idField := strings.Fields(strings.TrimPrefix(string(buf[:n]), "goroutine "))[0]
	id, err := strconv.Atoi(idField)
	if err != nil {
		panic(fmt.Sprintf("cannot get goroutine id: %v", err))
	}
	return id
}

var (
	stepSleep = 10 * time.Millisecond
	waitSleep = 50 * time.Millisecond
)

var ab = []string{"a", "b"}

func RunTransactionsTest(t *testing.T, ctx context.Context, db *sql.DB, txs [][]Query, wantStarts, wantEnds []string) {
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)
	// logger.Printf("GOMAXPROCS: %d\n", runtime.GOMAXPROCS(0))

	logger := log.New(os.Stdout, "", log.Ltime|log.Lmicroseconds)

	gotStarts := make([]string, 0)
	gotEnds := make([]string, 0)

	channels := make([]chan struct{}, len(txs))

	ran := -1

	for i := range txs {
		// logger.Printf("start ch%d\n", i)
		channels[i] = make(chan struct{})

		// logger.Printf("tx%d BeginTx\n", i)
		// tx, err := db.BeginTx(ctx, nil)
		conn, err := db.Conn(ctx)
		if err != nil {
			panic(err)
		}
		// _, err = conn.ExecContext(ctx, "SELECT 1") // ping
		// if err != nil {
		// 	panic(err)
		// }

		i := i

		go func() {
			// debug.PrintStack()
			goID := getGoID()
			// fmt.Println("goroutine started")
			// conn := conn

			ch := channels[i]
			defer close(ch)

			queries := txs[i]

			for j, q := range queries {
				ch <- struct{}{} // 1つ目のクエリを並行実行してしまわないように最後ではなく最初に同期

				logger.Printf("(go %d) start %s>[%d] %s\n", goID, ab[i], j, q.Query)
				gotStarts = append(gotStarts, fmt.Sprintf("%s:%d", ab[i], j))
				// start := time.Now()
				ran = i
				rows, err := conn.QueryContext(ctx, q.Query)
				ch <- struct{}{} // 結果を待つために同期
				logger.Printf("(go %d) end   %s<[%d] %s\n", goID, ab[i], j, q.Query)
				if err != nil {
					if q.WantErr != "" && err.Error() == q.WantErr {
						// ok, but break
						logger.Printf("(go %d) err   %s<[%d] %s\n", goID, ab[i], j, err)
					} else if err == sql.ErrNoRows && q.Want == nil && q.WantErr == "" {
						// ok
					} else {
						// fmt.Printf("%#v\n", err)
						// t.Error(err)
						fmt.Println(q.WantErr)
						fmt.Println(err)
						panic(err)
					}
				} else {
					got := make([]sql.NullInt64, 0)
					for rows.Next() {
						var c sql.NullInt64
						err = rows.Scan(&c)
						if err != nil {
							break
						}
						got = append(got, c)
					}
					logger.Printf("(go %d) got   %s<[%d] %+v\n", goID, ab[i], j, got)

					if q.Want != nil && !reflect.DeepEqual(got, q.Want) {
						t.Errorf("query %s:%d got=%+v, want=%+v", ab[i], j, got, q.Want)
					}
					if q.WantErr != "" {
						t.Errorf("query %s:%d got=%+v, wantErr=%s", ab[i], j, got, q.WantErr)
					}
				}

				// ch <- struct{}{}
				// if time.Since(start) > 10*time.Millisecond {
				if ran != i {
					// additional sleep after lock
					logger.Printf("(go %d) additional sleep after lock\n", goID)
					time.Sleep(stepSleep)
				}
				logger.Printf("(go %d) append %s<[%d] %s\n", goID, ab[i], j, q.Query)
				gotEnds = append(gotEnds, fmt.Sprintf("%s:%d", ab[i], j))
				if err != nil && err != sql.ErrNoRows {
					break
				}
			}

			// err = tx.Commit()
			// if err != nil {
			// 	panic(err)
			// }
		}()
		// logger.Printf("tx%d runtime.Gosched()\n", i)
		runtime.Gosched()
	}

	// time.Sleep(1 * time.Second)
	running := true
	for {
		running = false
		// deadlock := true
		for i, ch := range channels {
			// logger.Printf("ch%d Gosched()\n", i)
			runtime.Gosched()
			// time.Sleep(sleepMs)

			logger.Printf("ch%d step\n", i)

			select {
			case _, ok := <-ch:
				if ok {
					logger.Printf("ch%d stepped\n", i)
					running = true
					// deadlock = false
					select {
					case <-ch:
						logger.Printf("ch%d 2nd stepped\n", i)
						time.Sleep(stepSleep)
					case <-time.After(waitSleep):
						logger.Printf("ch%d 2nd timeout\n", i)
					}
				} else {
					logger.Printf("ch%d done\n", i)
				}
			// default:
			case <-time.After(waitSleep):
				logger.Printf("ch%d waiting\n", i)
				running = true
			}
		}

		if !running {
			break
		}

		// if deadlock {
		// 	panic("deadlock")
		// }

		select {
		case <-ctx.Done():
			panic(ctx.Err())
		default:
		}
	}

	if diff := cmp.Diff(wantStarts, gotStarts); diff != "" {
		t.Errorf("gotStarts mismatch (-want +got):\n%s", diff)
	}

	if wantEnds != nil {
		if diff := cmp.Diff(wantEnds, gotEnds); diff != "" {
			t.Errorf("gotEnds mismatch (-want +got):\n%s", diff)
		}
	}
}
