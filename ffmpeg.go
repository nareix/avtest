
package main

/*
#cgo LDFLAGS: -lavformat -lavutil -lavcodec
#cgo CFLAGS: -Werror
#include <libavformat/avformat.h>

const int AAC = CODEC_ID_AAC;
const int H264 = CODEC_ID_H264;

typedef struct {
	AVFormatContext *fmt_ctx;
	AVPacket *pkt;
} ffctx_t;

void ffctx_openfile(ffctx_t *ctx, const char *filename) {
	av_register_all();
	avformat_open_input(&ctx->fmt_ctx, filename, NULL, NULL);
	//av_dump_format(ctx->fmt_ctx, 0, filename, 0);
	AVPacket pkt;

	for (int i = 0; i < ctx->fmt_ctx->nb_streams; i++) {
		AVStream *stream = ctx->fmt_ctx->streams[i];
	}

	ctx->pkt = malloc(sizeof(AVPacket));
}

void ffctx_close(ffctx_t *ctx) {
	free(ctx->pkt);
	avformat_close_input(&ctx->fmt_ctx);
}

void ffctx_get_stream_codec_extradata(ffctx_t *ctx, int i, void **p, int *len, int *p_id) {
	AVStream *stream = ctx->fmt_ctx->streams[i];
	*p = stream->codec->extradata;
	*len = stream->codec->extradata_size;
	*p_id = stream->codec->codec_id;
}

void ffctx_read_frame(ffctx_t *ctx) {
	av_read_frame(ctx->fmt_ctx, ctx->pkt);
}

void ffctx_free_packet(ffctx_t *ctx) {
	av_free_packet(ctx->pkt);
}

*/
import "C"

import (
	"unsafe"
	"fmt"
	_ "github.com/nareix/av"
)

type FFMpegStream struct {
}

func init() {
	ctx := &C.ffctx_t{}
	var p unsafe.Pointer
	var size, id C.int

	fmt.Printf("ffmpeg: AAC=%d\n", C.AAC)
	fmt.Printf("ffmpeg: H264=%d\n", C.H264)

	C.ffctx_openfile(ctx, C.CString("projectindex.mp4"))

	C.ffctx_get_stream_codec_extradata(ctx, 0, &p, &size, &id)
	fmt.Println("ffmpeg: CodecData size", size)

	C.ffctx_get_stream_codec_extradata(ctx, 1, &p, &size, &id)
	fmt.Println("ffmpeg: CodecData size", size)

	C.ffctx_get_stream_codec_extradata(ctx, 1, &p, &size, &id)
	fmt.Println("ffmpeg: CodecData size", size)

	C.ffctx_read_frame(ctx)
	C.ffctx_free_packet(ctx)
	C.ffctx_read_frame(ctx)
	C.ffctx_free_packet(ctx)
	C.ffctx_read_frame(ctx)
	C.ffctx_free_packet(ctx)
	C.ffctx_read_frame(ctx)
	C.ffctx_free_packet(ctx)
}


