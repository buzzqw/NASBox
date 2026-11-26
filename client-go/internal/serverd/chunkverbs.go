package serverd

import (
	"fmt"
	"strings"
)

func cmdChunkStats(ctx *Context) error {
	store, err := ctx.chunkStore()
	if err != nil {
		return err
	}
	objects, bytes, err := store.Stats()
	if err != nil {
		return err
	}
	snapshots, err := store.ListSnapshots()
	if err != nil {
		return err
	}
	fmt.Println("CHUNK_STORE_V1")
	fmt.Printf("OBJECTS=%d\n", objects)
	fmt.Printf("BYTES=%d\n", bytes)
	fmt.Printf("SNAPSHOTS=%d\n", len(snapshots))
	fmt.Printf("CHUNK_SIZE=%d\n", ctx.ChunkSize())
	return nil
}

func cmdChunkSnapshotCreate(ctx *Context, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("uso: --chunk-snapshot-create <device> <path> [path...]")
	}
	store, err := ctx.chunkStore()
	if err != nil {
		return err
	}
	snap, err := store.CreateSnapshot(ctx.ShareRoot, args[1:], args[0])
	if err != nil {
		return err
	}
	chunks := 0
	for _, file := range snap.Files {
		chunks += len(file.Chunks)
	}
	fmt.Println("SNAPSHOT_V1")
	fmt.Println("ID=" + snap.ID)
	fmt.Printf("FILES=%d\nCHUNKS=%d\n", len(snap.Files), chunks)
	return nil
}

func cmdChunkSnapshotList(ctx *Context) error {
	store, err := ctx.chunkStore()
	if err != nil {
		return err
	}
	ids, err := store.ListSnapshots()
	if err != nil {
		return err
	}
	fmt.Println("SNAPSHOT_LIST_V1")
	for _, id := range ids {
		fmt.Println(id)
	}
	return nil
}

func cmdChunkRestore(ctx *Context, args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("uso: --chunk-restore <snapshot-id> <path> <destinazione>")
	}
	store, err := ctx.chunkStore()
	if err != nil {
		return err
	}
	snap, err := store.LoadSnapshot(strings.TrimSpace(args[0]))
	if err != nil {
		return err
	}
	if err := store.RestoreFile(snap, args[1], args[2]); err != nil {
		return err
	}
	fmt.Println("CHUNK_RESTORE_V1")
	fmt.Println("OK")
	return nil
}
