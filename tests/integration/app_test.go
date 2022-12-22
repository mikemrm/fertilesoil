package integration_test

import (
	"context"
	"testing"

	natsgo "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"

	apiv1 "github.com/infratographer/fertilesoil/api/v1"
	clientv1nats "github.com/infratographer/fertilesoil/client/v1/nats"
	"github.com/infratographer/fertilesoil/notifier/nats"
	natsutils "github.com/infratographer/fertilesoil/notifier/nats/utils"
)

func TestAppReconcileAndWatch(t *testing.T) {
	t.Parallel()

	// initialize socket to communicate with the tree manager
	skt := newUnixsocketPath(t)

	// initialize NATS server for notifications
	natss, natserr := natsutils.StartNatsServer()
	assert.NoError(t, natserr, "error starting nats server")

	defer natss.Shutdown()

	conn, err := natsgo.Connect(natss.ClientURL())
	assert.NoError(t, err, "connecting to nats server")

	clientconn, err := natsgo.Connect(natss.ClientURL())
	assert.NoError(t, err, "connecting to nats server")

	natsutils.WaitConnected(t, conn)
	natsutils.WaitConnected(t, clientconn)

	subject := t.Name()

	// build notifier
	ntf, err := nats.NewNotifier(conn, subject)
	assert.NoError(t, err, "creating nats notifier")

	// Build tree manager server
	srv := newTestServerWithNotifier(t, skt, ntf)
	defer func() {
		err := srv.Shutdown()
		assert.NoError(t, err, "error shutting down server")
	}()

	go runTestServer(t, srv)

	cli := newTestClient(t, skt)

	waitForServer(t, cli)

	// initialize root. An app needs a root to be initialized
	rd, err := cli.CreateRoot(context.Background(), &apiv1.CreateDirectoryRequest{
		DirectoryRequestMeta: apiv1.DirectoryRequestMeta{
			Version: apiv1.APIVersion,
		},
		Name: "root",
	})
	assert.NoError(t, err, "error creating root")

	// Set up test application
	appstore := setupAppStorage(t)

	watcher, err := clientv1nats.NewSubscriber(clientconn, subject)
	assert.NoError(t, err, "error creating nats subscriber")

	appctrl, apptester := setupTestApp(t, rd.Directory.ID, cli, watcher, appstore)

	cancelCtx, cancel := context.WithCancel(context.Background())

	// Run application controller. This will initialize the application and
	// start watching for changes
	go func() {
		runerr := appctrl.Run(cancelCtx)
		assert.ErrorIs(t, runerr, context.Canceled, "expected context canceled error")
	}()

	apptester.waitForReconcile()

	evts := apptester.popEvents()

	// We should have done one reconcile for the root
	assert.Len(t, evts, 1, "expected 1 event")

	// We should have created the root
	assert.Equal(t, apiv1.EventTypeCreate, evts[0].Type, "expected created event")

	// We should only have one reconcile call
	assert.Equal(t, apptester.getReconcileCalls(), uint32(1), "expected 1 reconcile call")

	// Create a directory
	_, err = cli.CreateDirectory(context.Background(), &apiv1.CreateDirectoryRequest{
		DirectoryRequestMeta: apiv1.DirectoryRequestMeta{
			Version: apiv1.APIVersion,
		},
		Name: "test",
	}, rd.Directory.ID)
	assert.NoError(t, err, "error creating directory")

	apptester.waitForReconcile()

	evts = apptester.popEvents()

	// We should have done one reconcile for the new directory
	assert.Len(t, evts, 1, "expected 1 event")

	// We should have created the directory
	assert.Equal(t, apiv1.EventTypeCreate, evts[0].Type, "expected created event")

	// We should only have two reconcile calls
	assert.Equal(t, apptester.getReconcileCalls(), uint32(2), "expected 2 reconcile calls")

	// wait for a full reconcile
	apptester.waitForReconcile()

	// we should have 2 reconcile calls
	// as the directories are up-to-date
	assert.Equal(t, apptester.getReconcileCalls(), uint32(2), "expected 2 reconcile calls")

	cancel()
}
