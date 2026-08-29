#ifdef __OBJC__
#import <UIKit/UIKit.h>
#else
#ifndef FOUNDATION_EXPORT
#if defined(__cplusplus)
#define FOUNDATION_EXPORT extern "C"
#else
#define FOUNDATION_EXPORT extern
#endif
#endif
#endif

#import "markdown_lib.h"
#import "markdown_peg.h"
#import "platform.h"

FOUNDATION_EXPORT double AttributedMarkdownVersionNumber;
FOUNDATION_EXPORT const unsigned char AttributedMarkdownVersionString[];

