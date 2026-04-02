package kmongo_test

import (
	"context"
	"os"
	"testing"
	"time"

	kitsune "github.com/zenbaku/go-kitsune"
	"github.com/zenbaku/go-kitsune/tails/kmongo"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func skipIfNoMongo(t *testing.T) *mongo.Client {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI not set; skipping MongoDB integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Skipf("mongo.Connect: %v", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		t.Skipf("mongo ping: %v", err)
	}
	return client
}

type testDoc struct {
	ID    bson.ObjectID `bson:"_id,omitempty"`
	Value int           `bson:"value"`
}

func TestFindAndInsertMany(t *testing.T) {
	client := skipIfNoMongo(t)
	defer func() { _ = client.Disconnect(context.Background()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	coll := client.Database("kitsune_test").Collection("kmongo_test_" + t.Name())
	t.Cleanup(func() { _ = coll.Drop(context.Background()) })

	// Insert 5 documents.
	docs := []testDoc{{Value: 1}, {Value: 2}, {Value: 3}, {Value: 4}, {Value: 5}}
	insertSink := kmongo.InsertMany(coll, func(d testDoc) (any, error) { return d, nil })
	if err := insertSink(ctx, docs); err != nil {
		t.Fatal(err)
	}

	// Find them back.
	got, err := kmongo.Find(coll, bson.M{}, func(cur *mongo.Cursor) (testDoc, error) {
		var d testDoc
		return d, cur.Decode(&d)
	}).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5 docs, got %d", len(got))
	}
}

func TestFindTake(t *testing.T) {
	client := skipIfNoMongo(t)
	defer func() { _ = client.Disconnect(context.Background()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	coll := client.Database("kitsune_test").Collection("kmongo_take_" + t.Name())
	t.Cleanup(func() { _ = coll.Drop(context.Background()) })

	docs := []testDoc{{Value: 10}, {Value: 20}, {Value: 30}, {Value: 40}, {Value: 50}}
	insertSink := kmongo.InsertMany(coll, func(d testDoc) (any, error) { return d, nil })
	if err := insertSink(ctx, docs); err != nil {
		t.Fatal(err)
	}

	got, err := kitsune.Map(
		kmongo.Find(coll, bson.M{}, func(cur *mongo.Cursor) (testDoc, error) {
			var d testDoc
			return d, cur.Decode(&d)
		}),
		func(_ context.Context, d testDoc) (int, error) { return d.Value, nil },
	).Take(3).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
}
