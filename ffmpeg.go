
package main

/*
#cgo LDFLAGS: -lavformat -lavutil -lavcodec
#cgo CFLAGS: -Werror
#include <libavformat/avformat.h>

typedef struct {
	AVFormatContext *fmt_ctx;
	AVPacket pkt;
} ffctx_t;

void ffctx_openfile(ffctx_t *ctx, const char *filename) {
	av_register_all();
	avformat_open_input(&ctx->fmt_ctx, filename, NULL, NULL);
	//av_dump_format(ctx->fmt_ctx, 0, filename, 0);
	AVPacket pkt;

	#if 0
	for (int i = 0; i < ctx->fmt_ctx->nb_streams; i++) {
		AVStream *stream = ctx->fmt_ctx->streams[i];
		printf("stream=%d extradata_size=%d\n", i, stream->codec->extradata_size);
	}

	int i = 0;
	while (av_read_frame(ctx->fmt_ctx, &pkt) >= 0) {
		//printf("i=%d stream_index=%d\n", i, pkt.stream_index);
		av_free_packet(&pkt);
		i++;
	}
	#endif
}

void ffctx_get_stream_codec_extradata(ffctx_t *ctx, int i, void **p, int *len) {
	AVStream *stream = ctx->fmt_ctx->streams[i];
	*p = stream->codec->extradata;
	*len = stream->codec->extradata_size;
}

void ffctx_read_frame(ffctx_t *ctx) {
	av_read_frame(ctx->fmt_ctx, &ctx->pkt);
}

void ffctx_free_packet(ffctx_t *ctx) {
	//av_free_packet(&ctx->pkt);
}

*/
import "C"
import "unsafe"

func init() {
	ctx := &C.ffctx_t{}
	var p unsafe.Pointer
	var size C.int

	C.ffctx_openfile(ctx, C.CString("test.mp4"))
	C.ffctx_get_stream_codec_extradata(ctx, 0, &p, &size)
	b := C.GoBytes(p, size)
	println("ffmpeg", b)
}

