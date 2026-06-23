#!/usr/bin/env sh
# Launch uvicorn workers so the task's cores are fully used WITHOUT
# oversubscription, which is what actually governs OCR throughput.
#
# Each OCR inference can use up to OCR_ORT_INTRA_THREADS cores (ONNX Runtime
# intra-op pool). Thread scaling is near-linear, so this is a pure
# latency<->in-task-concurrency knob at a fixed core budget:
#
#   intra=8 -> ~450ms/img, 1 image at a time per 8-core task
#   intra=4 -> ~760ms/img, 2 images at a time
#   intra=2 -> ~1.4s/img,  4 images at a time
#   intra=1 -> ~2.7s/img,  8 images at a time (max throughput-per-core)
#
# Total throughput-per-core is ~flat across these, so raw images/sec is scaled
# by ADDING TASKS, not by this knob. We therefore run
#   workers = cores / intra_threads
# to keep every core busy at the chosen latency point. Each worker loads its own
# model (~1.5 GB) — size task memory as workers x 1.5 GB.
set -eu

# Detect cores available to this container (respects cgroup CPU quota when set).
detect_cpus() {
    # cgroup v2
    if [ -r /sys/fs/cgroup/cpu.max ]; then
        quota=$(cut -d' ' -f1 /sys/fs/cgroup/cpu.max)
        period=$(cut -d' ' -f2 /sys/fs/cgroup/cpu.max)
        if [ "$quota" != "max" ] && [ "$period" -gt 0 ] 2>/dev/null; then
            cpus=$(( (quota + period - 1) / period ))
            [ "$cpus" -ge 1 ] && { echo "$cpus"; return; }
        fi
    fi
    # cgroup v1
    if [ -r /sys/fs/cgroup/cpu/cpu.cfs_quota_us ] && [ -r /sys/fs/cgroup/cpu/cpu.cfs_period_us ]; then
        quota=$(cat /sys/fs/cgroup/cpu/cpu.cfs_quota_us)
        period=$(cat /sys/fs/cgroup/cpu/cpu.cfs_period_us)
        if [ "$quota" -gt 0 ] 2>/dev/null && [ "$period" -gt 0 ] 2>/dev/null; then
            cpus=$(( (quota + period - 1) / period ))
            [ "$cpus" -ge 1 ] && { echo "$cpus"; return; }
        fi
    fi
    nproc 2>/dev/null || echo 1
}

CPUS="$(detect_cpus)"
INTRA="${OCR_ORT_INTRA_THREADS:-4}"
[ "$INTRA" -ge 1 ] 2>/dev/null || INTRA=1
export OCR_ORT_INTRA_THREADS="$INTRA"

# workers = cores / intra (override with OCR_PROC_WORKERS). Keep >=1.
if [ -n "${OCR_PROC_WORKERS:-}" ]; then
    WORKERS="$OCR_PROC_WORKERS"
else
    WORKERS=$(( CPUS / INTRA ))
fi
[ "$WORKERS" -ge 1 ] 2>/dev/null || WORKERS=1

export OMP_NUM_THREADS="${OMP_NUM_THREADS:-1}"
export MKL_NUM_THREADS="${MKL_NUM_THREADS:-1}"

echo "ocr_sidecar: cores=${CPUS} intra_op=${INTRA} -> ${WORKERS} worker process(es)"
exec uvicorn main:app --host 0.0.0.0 --port 8000 --workers "${WORKERS}"
