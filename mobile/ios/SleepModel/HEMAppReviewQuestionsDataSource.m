//
//  HEMAppReviewQuestionsDataSource.m
//  Sense
//
//  Created by Jimmy Lu on 7/30/15.
//  Copyright (c) 2015 Hello. All rights reserved.
//

#import <SenseKit/SENAPIAppFeedback.h>

#import "HEMAppReviewQuestionsDataSource.h"
#import "HEMAppReview.h"
#import "HEMSupportUtil.h"
#import "HEMSettingsNavigationController.h"
#import "HEMQuestionsService.h"

static NSString* const HEMQuestionCellIdSingle = @"single";
static NSString* const HEMAppReviewFeedbackTopic = @"feedback";

@interface HEMAppReviewQuestionsDataSource() <MFMailComposeViewControllerDelegate>

@property (nonatomic, strong) HEMAppReviewQuestion* currentReviewQuestion;
@property (nonatomic, strong) HEMAppReviewAnswer* selectedAnswer;
@property (nonatomic, weak)   UIViewController* controller;
@property (nonatomic, weak)   HEMQuestionsService* service;

// for app feedback
@property (nonatomic, assign) SENAppReviewFeedback feedback;
@property (nonatomic, assign) BOOL attemptedToReview;

@end

@implementation HEMAppReviewQuestionsDataSource

- (instancetype)initWithAppReviewQuestion:(HEMAppReviewQuestion*)appReviewQuestion
                                  service:(HEMQuestionsService*)questionsService {
    self = [super init];
    if (self) {
        _currentReviewQuestion = appReviewQuestion;
    }
    return self;
}

- (HEMAppReviewAnswer*)answerAtIndexPath:(NSIndexPath*)indexPath {
    NSArray* answers = [[self currentReviewQuestion] choices];
    HEMAppReviewAnswer* answer = nil;
    if ([indexPath row] < [answers count]) {
        answer = answers[[indexPath row]];
    }
    return answer;
}

- (NSString*)selectedQuestionText {
    return [[self currentReviewQuestion] text];
}

- (NSString*)answerTextAtIndexPath:(NSIndexPath*)indexPath {
    HEMAppReviewAnswer* answer = [self answerAtIndexPath:indexPath];
    return [answer answer];
}

/**
 * @discussion
 * App review questions are never multiple choice.
 */
- (BOOL)allowMultipleSelectionForSelectedQuestion {
    return NO;
}

- (void)nextQuestion {
    switch ([[self selectedAnswer] action]) {
        case HEMAppReviewAnswerActionEnjoySense:
            [SENAnalytics track:HEMAnalyticsEventAppReviewEnjoySense];
            break;
        case HEMAppReviewAnswerActionDoNotEnjoySense:
            [SENAnalytics track:HEMAnalyticsEventAppReviewDoNotEnjoySense];
            break;
        default:
            break;
    }
    
    HEMAppReviewQuestion* next = [[self currentReviewQuestion] nextQuestionForAnswer:[self selectedAnswer]];
    [self setCurrentReviewQuestion:next];
}

/**
 * @discussion
 * Skipping the question is the same as if user declined to provide feedback or
 * write a review for the app and thus we should not need to proceed and should
 * mark as completed
 */
- (BOOL)skipQuestion {
    [[self service] skipQuestion:[self currentReviewQuestion] completion:nil];
    [self setSelectedAnswer:nil];
    return NO;
}

- (BOOL)selectAnswerAtIndexPath:(NSIndexPath*)indexPath {
    HEMAppReviewAnswer* answer = [self answerAtIndexPath:indexPath];
    [self setSelectedAnswer:answer];
    
    BOOL hasAnotherQuestion = NO;
    
    if ([answer action] == HEMAppReviewAnswerActionEnjoySense) {
        [self setFeedback:SENAppReviewFeedbackLikeIt];
        hasAnotherQuestion = YES;
    } else if ([answer action] == HEMAppReviewAnswerActionDoNotEnjoySense) {
        [self setFeedback:SENAppReviewFeedbackDoNotLikeIt];
        hasAnotherQuestion = YES;
    } else if ([answer action] == HEMAppReviewAnswerActionOpenSupport) {
        [self setFeedback:SENAppReviewFeedbackNeedHelp];
    } else if ([answer action] == HEMAppReviewAnswerActionRateTheApp) {
        [self setAttemptedToReview:YES];
    }
    
    return hasAnotherQuestion;
}

/**
 * @discussion
 * App review questions only support choice answers, not multi selection so set
 * should only contain 1 object
 */
- (BOOL)selectAnswersAtIndexPaths:(NSSet *)indexPaths {
    NSIndexPath* indexPath = [[indexPaths objectEnumerator] nextObject];
    return [self selectAnswerAtIndexPath:indexPath];
}

- (BOOL)isIndexPathLast:(NSIndexPath*)indexPath {
    NSArray* answers = [[self currentReviewQuestion] choices];
    return [indexPath row] == [answers count] - 1;
}

- (BOOL)takeActionBeforeDismissingFrom:(UIViewController*)controller {
    [HEMAppReview markAppReviewPromptCompleted];
    
    [self setController:controller];
    
    switch ([[self selectedAnswer] action]) {
        case HEMAppReviewAnswerActionOpenSupport: {
            // wait for ticket to be created before sending feedback since user
            // can change their mind
            // Support requests go out by email now; the completion is the
            // mail composer finishing rather than a Zendesk submission event.
            static NSString* const internalSubject = @"iOS App Review Help";
            [HEMSupportUtil sendEmailTo:NSLocalizedString(@"help.email.address", nil)
                            withSubject:internalSubject
                              attachLog:YES
                                   from:controller
                           mailDelegate:self];
            return YES;
        }
        case HEMAppReviewAnswerActionRateTheApp: {
            [self listenToForAppComingBackToForeground];
            [self sendAppFeedback];
            switch ([[self currentReviewQuestion] reviewType]) {
                case HEMAppReviewTypeAmazon:
                    [SENAnalytics track:HEMAnalyticsEventAppReviewRateOnAmazon];
                    break;
                case HEMAppReviewTypeAppStore:
                default:
                    [SENAnalytics track:HEMAnalyticsEventAppReviewRate];
                    break;
            }
            [HEMAppReview rateApp:[[self currentReviewQuestion] reviewType]];
            return YES;
        }
        case HEMAppReviewAnswerActionSendFeedback: {
            // wait for ticket to be created before sending feedback since user
            // can change their mind
            [HEMSupportUtil sendEmailTo:NSLocalizedString(@"help.email.address", nil)
                            withSubject:HEMAppReviewFeedbackTopic
                              attachLog:YES
                                   from:controller
                           mailDelegate:self];
            return YES;
        }
        case HEMAppReviewAnswerActionStopAsking: {
            [self sendAppFeedback];
            [SENAnalytics track:HEMAnalyticsEventAppReviewRateNoAsk];
            [HEMAppReview stopAskingToRateTheApp];
            return NO;
        }
        case HEMAppReviewAnswerActionDone: {
            [self sendAppFeedback];
            [SENAnalytics track:HEMAnalyticsEventAppReviewDone];
            return NO;
        }
        default:
            [self sendAppFeedback];
            return NO;
    }
}

- (void)listenToForAppComingBackToForeground {
    NSNotificationCenter* center = [NSNotificationCenter defaultCenter];
    [center addObserver:self
               selector:@selector(didComeBackToForeground)
                   name:UIApplicationDidBecomeActiveNotification
                 object:nil];
}

- (void)didComeBackToForeground {
    [[self controller] dismissViewControllerAnimated:YES completion:nil];
}

- (void)mailComposeController:(MFMailComposeViewController *)mailController
          didFinishWithResult:(MFMailComposeResult)result
                        error:(NSError *)error {
    [mailController dismissViewControllerAnimated:YES completion:nil];
    // Only count it as feedback if the user actually sent it, which is what
    // the old ticket-submission notification signalled.
    if (result == MFMailComposeResultSent) {
        [self didSendSupportEmail];
    }
}

- (void)didSendSupportEmail {
    switch ([[self selectedAnswer] action]) {
        case HEMAppReviewAnswerActionSendFeedback:
            [SENAnalytics track:HEMAnalyticsEventAppReviewFeedback];
            break;
        case HEMAppReviewAnswerActionOpenSupport:
            [SENAnalytics track:HEMAnalyticsEventAppReviewHelp];
            break;
        default:
            break;
    }
    
    [self sendAppFeedback];
    [[self controller] dismissViewControllerAnimated:YES completion:nil];
}

- (void)sendAppFeedback {
    DDLogVerbose(@"sending feedback %ld, review %@",
                 (long)[self feedback], [self attemptedToReview]?@"y":@"n");
    [SENAPIAppFeedback sendAppFeedback:[self feedback]
                           reviewedApp:[self attemptedToReview]
                            completion:nil];
}

#pragma mark - UITableViewDataSource

- (NSInteger)tableView:(UITableView *)tableView numberOfRowsInSection:(NSInteger)section {
    return [[[self currentReviewQuestion] choices] count];
}

- (UITableViewCell*)tableView:(UITableView *)tableView cellForRowAtIndexPath:(NSIndexPath *)indexPath {
    return [tableView dequeueReusableCellWithIdentifier:HEMQuestionCellIdSingle];
}

#pragma mark - Clean up

- (void)dealloc {
    [[NSNotificationCenter defaultCenter] removeObserver:self];
}

@end
