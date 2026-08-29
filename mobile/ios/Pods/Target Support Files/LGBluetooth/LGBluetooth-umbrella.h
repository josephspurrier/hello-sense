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

#import "CBUUID+StringExtraction.h"
#import "LGBluetooth.h"
#import "LGCentralManager.h"
#import "LGCharacteristic.h"
#import "LGPeripheral.h"
#import "LGService.h"
#import "LGUtils.h"

FOUNDATION_EXPORT double LGBluetoothVersionNumber;
FOUNDATION_EXPORT const unsigned char LGBluetoothVersionString[];

