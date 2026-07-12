package transport

// Writer is the minimal write surface PrioritySender needs — satisfied by
// *net.UDPConn directly, or by a wrapper (e.g. security.SealingWriter) that
// encrypts each payload before handing it to the socket.
type Writer interface {
	Write(b []byte) (int, error)
}

// PrioritySender serializes writes to a shared UDP connection so that
// time-sensitive vitals packets are never queued behind a burst of
// lower-priority media chunks. Vitals and media chunks are otherwise sent
// by independent goroutines with no natural ordering between them; a large
// image/audio transfer in progress could crowd out a heart-rate reading
// that matters moment-to-moment. Use VitalsWrite as the write callback for
// vitals sends and MediaWrite as transport.MediaSender.Write.
type PrioritySender struct {
	vitalsCh chan sendJob
	mediaCh  chan sendJob
	done     chan struct{}
}

type sendJob struct {
	data []byte
	err  chan error
}

func NewPrioritySender(w Writer) *PrioritySender {
	p := &PrioritySender{
		vitalsCh: make(chan sendJob, 4),
		mediaCh:  make(chan sendJob, 4),
		done:     make(chan struct{}),
	}
	go p.run(w)
	return p
}

func (p *PrioritySender) run(w Writer) {
	for {
		// Drain any waiting vitals job first, every iteration, so a vitals
		// send can never be stuck behind a media chunk that's already
		// queued — vitals always jumps the line.
		select {
		case job := <-p.vitalsCh:
			_, err := w.Write(job.data)
			job.err <- err
			continue
		case <-p.done:
			return
		default:
		}

		select {
		case job := <-p.vitalsCh:
			_, err := w.Write(job.data)
			job.err <- err
		case job := <-p.mediaCh:
			_, err := w.Write(job.data)
			job.err <- err
		case <-p.done:
			return
		}
	}
}

// VitalsWrite sends data with vitals (high) priority.
func (p *PrioritySender) VitalsWrite(data []byte) error {
	job := sendJob{data: data, err: make(chan error, 1)}
	p.vitalsCh <- job
	return <-job.err
}

// MediaWrite sends data with media (low) priority — use as MediaSender.Write.
func (p *PrioritySender) MediaWrite(data []byte) error {
	job := sendJob{data: data, err: make(chan error, 1)}
	p.mediaCh <- job
	return <-job.err
}

// Close stops the sender's background goroutine. Safe to call once.
func (p *PrioritySender) Close() {
	close(p.done)
}
